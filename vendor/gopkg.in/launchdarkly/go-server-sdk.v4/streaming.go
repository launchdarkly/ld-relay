package ldclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-server-sdk.v4/internal"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

const (
	putEvent           = "put"
	patchEvent         = "patch"
	deleteEvent        = "delete"
	indirectPatchEvent = "indirect/patch"
	streamReadTimeout  = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
)

type streamProcessor struct {
	store              FeatureStore
	client             *http.Client
	requestor          *requestor
	config             Config
	sdkKey             string
	setInitializedOnce sync.Once
	isInitialized      bool
	halt               chan struct{}
	storeStatusSub     internal.FeatureStoreStatusSubscription
	readyOnce          sync.Once
	closeOnce          sync.Once
}

type putData struct {
	Path string  `json:"path"`
	Data allData `json:"data"`
}

type allData struct {
	Flags    map[string]*FeatureFlag `json:"flags"`
	Segments map[string]*Segment     `json:"segments"`
}

type patchData struct {
	Path string `json:"path"`
	// This could be a flag or a segment, or something else, depending on the path
	Data json.RawMessage `json:"data"`
}

type deleteData struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

func (sp *streamProcessor) Initialized() bool {
	return sp.isInitialized
}

func (sp *streamProcessor) Start(closeWhenReady chan<- struct{}) {
	sp.config.Loggers.Info("Starting LaunchDarkly streaming connection")
	if fss, ok := sp.store.(internal.FeatureStoreStatusProvider); ok {
		sp.storeStatusSub = fss.StatusSubscribe()
	}
	go sp.subscribe(closeWhenReady)
}

type parsedPath struct {
	key  string
	kind VersionedDataKind
}

func parsePath(path string) (parsedPath, error) {
	parsedPath := parsedPath{}
	if strings.HasPrefix(path, "/segments/") {
		parsedPath.kind = Segments
		parsedPath.key = strings.TrimPrefix(path, "/segments/")
	} else if strings.HasPrefix(path, "/flags/") {
		parsedPath.kind = Features
		parsedPath.key = strings.TrimPrefix(path, "/flags/")
	} else {
		return parsedPath, fmt.Errorf("unrecognized path %s", path)
	}
	return parsedPath, nil
}

// Returns true if we should recreate the stream and start over
func (sp *streamProcessor) events(stream *es.Stream, closeWhenReady chan<- struct{}) bool {
	notifyReady := func() {
		sp.readyOnce.Do(func() {
			close(closeWhenReady)
		})
	}
	// Ensure we stop waiting for initialization if we exit, even if initialization fails
	defer notifyReady()

	// Consume remaining Events and Errors so we can garbage collect
	defer func() {
		for range stream.Events {
		}
		for range stream.Errors {
		}
	}()

	var statusCh <-chan internal.FeatureStoreStatus
	if sp.storeStatusSub != nil {
		statusCh = sp.storeStatusSub.Channel()
	}

	for {
		select {
		case event, ok := <-stream.Events:
			if !ok {
				sp.config.Loggers.Info("Event stream closed")
				return false
			}
			switch event.Event() {
			case putEvent:
				var put putData
				if err := json.Unmarshal([]byte(event.Data()), &put); err != nil {
					sp.config.Loggers.Errorf("Unexpected error unmarshalling PUT json: %+v", err)
					break
				}
				err := sp.store.Init(MakeAllVersionedDataMap(put.Data.Flags, put.Data.Segments))
				if err != nil {
					sp.config.Loggers.Errorf("Error initializing store: %s", err)
					return false
				}
				sp.setInitializedOnce.Do(func() {
					sp.config.Loggers.Info("LaunchDarkly streaming is active")
					sp.isInitialized = true
					notifyReady()
				})
			case patchEvent:
				var patch patchData
				if err := json.Unmarshal([]byte(event.Data()), &patch); err != nil {
					sp.config.Loggers.Errorf("Unexpected error unmarshalling PATCH json: %+v", err)
					break
				}
				path, err := parsePath(patch.Path)
				if err != nil {
					sp.config.Loggers.Errorf("Unable to process event %s: %s", event.Event(), err)
					break
				}
				item := path.kind.GetDefaultItem().(VersionedData)
				if err = json.Unmarshal(patch.Data, item); err != nil {
					sp.config.Loggers.Errorf("Unexpected error unmarshalling JSON for %s item: %+v", path.kind, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					sp.config.Loggers.Errorf("Unexpected error storing %s item: %+v", path.kind, err)
				}
			case deleteEvent:
				var data deleteData
				if err := json.Unmarshal([]byte(event.Data()), &data); err != nil {
					sp.config.Loggers.Errorf("Unexpected error unmarshalling DELETE json: %+v", err)
					break
				}
				path, err := parsePath(data.Path)
				if err != nil {
					sp.config.Loggers.Errorf("Unable to process event %s: %s", event.Event(), err)
					break
				}
				if err = sp.store.Delete(path.kind, path.key, data.Version); err != nil {
					sp.config.Loggers.Errorf(`Unexpected error deleting %s item "%s": %s`, path.kind, path.key, err)
				}
			case indirectPatchEvent:
				path, err := parsePath(event.Data())
				if err != nil {
					sp.config.Loggers.Errorf("Unable to process event %s: %s", event.Event(), err)
					break
				}
				item, requestErr := sp.requestor.requestResource(path.kind, path.key)
				if requestErr != nil {
					sp.config.Loggers.Errorf(`Unexpected error requesting %s item "%s": %+v`, path.kind, path.key, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					sp.config.Loggers.Errorf(`Unexpected error store %s item "%s": %+v`, path.kind, path.key, err)
				}
			default:
				sp.config.Loggers.Infof("Unexpected event found in stream: %s", event.Event())
			}
		case err, ok := <-stream.Errors:
			if !ok {
				sp.config.Loggers.Info("Event error stream closed")
				return false // Otherwise we will spin in this loop
			}
			if err != io.EOF {
				sp.config.Loggers.Errorf("Error encountered processing stream: %+v", err)
				if sp.checkIfPermanentFailure(err) {
					sp.closeOnce.Do(func() {
						sp.config.Loggers.Info("Closing event stream")
						stream.Close()
					})
					return false
				}
			}
		case newStoreStatus := <-statusCh:
			if newStoreStatus.Available && newStoreStatus.NeedsRefresh {
				// The store has just transitioned from unavailable to available, and we can't guarantee that
				// all of the latest data got cached, so let's restart the stream to refresh all the data.
				sp.config.Loggers.Warn("Restarting stream to refresh data after feature store outage")
				stream.Close()
				return true // causes subscribe() to restart the connection
			}
		case <-sp.halt:
			stream.Close()
			return false
		}
	}
}

func newStreamProcessor(sdkKey string, config Config, requestor *requestor) *streamProcessor {
	sp := &streamProcessor{
		store:     config.FeatureStore,
		config:    config,
		sdkKey:    sdkKey,
		requestor: requestor,
		halt:      make(chan struct{}),
	}

	sp.client = config.newHTTPClient()
	// Client.Timeout isn't just a connect timeout, it will break the connection if a full response
	// isn't received within that time (which, with the stream, it never will be), so we must make
	// sure it's zero and not the usual configured default. What we do want is a *connection* timeout,
	// which is set by newHTTPClient as a property of the Dialer.
	sp.client.Timeout = 0

	return sp
}

func (sp *streamProcessor) subscribe(closeWhenReady chan<- struct{}) {
	for {
		req, _ := http.NewRequest("GET", sp.config.StreamUri+"/all", nil)
		req.Header.Add("Authorization", sp.sdkKey)
		req.Header.Add("User-Agent", sp.config.UserAgent)
		sp.config.Loggers.Info("Connecting to LaunchDarkly stream")

		if stream, err := es.SubscribeWithRequestAndOptions(req,
			es.StreamOptionHTTPClient(sp.client),
			es.StreamOptionReadTimeout(streamReadTimeout),
			es.StreamOptionLogger(sp.config.Loggers.ForLevel(ldlog.Info))); err != nil {

			sp.config.Loggers.Warnf("Unable to establish streaming connection: %+v", err)

			if sp.checkIfPermanentFailure(err) {
				close(closeWhenReady)
				return
			}

			// Halt immediately if we've been closed already
			select {
			case <-sp.halt:
				close(closeWhenReady)
				return
			default:
				time.Sleep(2 * time.Second)
			}
		} else {
			if !sp.events(stream, closeWhenReady) {
				return
			}
			// If events() returned true, we should continue the for loop
		}
	}
}

func (sp *streamProcessor) checkIfPermanentFailure(err error) bool {
	if se, ok := err.(es.SubscriptionError); ok {
		sp.config.Loggers.Error(httpErrorMessage(se.Code, "streaming connection", "will retry"))
		if !isHTTPErrorRecoverable(se.Code) {
			return true
		}
	}
	return false
}

// Close instructs the processor to stop receiving updates
func (sp *streamProcessor) Close() error {
	sp.closeOnce.Do(func() {
		sp.config.Loggers.Info("Closing event stream")
		close(sp.halt)
		if sp.storeStatusSub != nil {
			sp.storeStatusSub.Close()
		}
	})
	return nil
}
