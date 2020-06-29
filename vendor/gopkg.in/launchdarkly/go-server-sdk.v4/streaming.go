package ldclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-server-sdk.v4/internal"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

const (
	putEvent                 = "put"
	patchEvent               = "patch"
	deleteEvent              = "delete"
	indirectPatchEvent       = "indirect/patch"
	streamReadTimeout        = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
	streamMaxRetryDelay      = 30 * time.Second
	streamRetryResetInterval = 60 * time.Second
	streamJitterRatio        = 0.5
	defaultStreamRetryDelay  = 1 * time.Second
)

type streamProcessor struct {
	store                      FeatureStore
	client                     *http.Client
	requestor                  *requestor
	config                     Config
	sdkKey                     string
	setInitializedOnce         sync.Once
	isInitialized              bool
	halt                       chan struct{}
	storeStatusSub             internal.FeatureStoreStatusSubscription
	connectionAttemptStartTime uint64
	connectionAttemptLock      sync.Mutex
	readyOnce                  sync.Once
	closeOnce                  sync.Once
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

// Process events from the stream until it's time to close the stream.
//
// This returns true if we should recreate the stream and start over, or false if we should give up and never retry.
//
// Error handling works as follows:
// 1. If any event is malformed, we must assume the stream is broken and we may have missed updates. Restart it.
// 2. If we try to put updates into the data store and we get an error, we must assume something's wrong with the
// data store.
// 2a. If the data store supports status notifications (which all persistent stores normally do), then we can
// assume it has entered a failed state and will notify us once it is working again. If and when it recovers, then
// it will tell us whether we need to restart the stream (to ensure that we haven't missed any updates), or
// whether it has already persisted all of the stream updates we received during the outage.
// 2b. If the data store doesn't support status notifications (which is normally only true of the in-memory store)
// then we don't know the significance of the error, but we must assume that updates have been lost, so we'll
// restart the stream.
// 3. If we receive an unrecoverable error like HTTP 401, we close the stream and don't retry. Any other HTTP
// error or network error causes a retry with backoff.
// 4. We close the closeWhenReady channel to tell the client initialization logic that initialization has either
// succeeded (we got an initial payload and successfully stored it) or permanently failed (we got a 401, etc.).
// Otherwise, the client initialization method may time out but we will still be retrying in the background, and
// if we succeed then the client can detect that we're initialized now by calling our Initialized method.
func (sp *streamProcessor) consumeStream(stream *es.Stream, closeWhenReady chan<- struct{}) {
	// Consume remaining Events and Errors so we can garbage collect
	defer func() {
		for range stream.Events {
		}
		if stream.Errors != nil {
			for range stream.Errors {
			}
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
				return // The stream only gets closed without an error happening if we're being shut down externally
			}
			sp.logConnectionResult(true)

			shouldRestart := false

			gotMalformedEvent := func(event es.Event, err error) {
				sp.config.Loggers.Errorf("Received streaming \"%s\" event with malformed JSON data (%s); will restart stream", event.Event(), err)
				shouldRestart = true // scenario 1 above
			}

			storeUpdateFailed := func(updateDesc string, err error) {
				if sp.storeStatusSub != nil {
					sp.config.Loggers.Errorf("Failed to store %s in data store (%s); will try again once data store is working", updateDesc, err)
					// scenario 2a above
				} else {
					sp.config.Loggers.Errorf("Failed to store %s in data store (%s); will restart stream until successful", updateDesc, err)
					shouldRestart = true // scenario 2b above
				}
			}

			switch event.Event() {
			case putEvent:
				var put putData
				if err := json.Unmarshal([]byte(event.Data()), &put); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				err := sp.store.Init(MakeAllVersionedDataMap(put.Data.Flags, put.Data.Segments))
				if err == nil {
					sp.setInitializedAndNotifyClient(true, closeWhenReady)
				} else {
					storeUpdateFailed("initial streaming data", err)
				}

			case patchEvent:
				var patch patchData
				if err := json.Unmarshal([]byte(event.Data()), &patch); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				path, err := parsePath(patch.Path)
				if err != nil {
					gotMalformedEvent(event, err)
					break
				}
				item := path.kind.GetDefaultItem().(VersionedData)
				if err = json.Unmarshal(patch.Data, item); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					storeUpdateFailed("streaming update of "+path.key, err)
				}

			case deleteEvent:
				var data deleteData
				if err := json.Unmarshal([]byte(event.Data()), &data); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				path, err := parsePath(data.Path)
				if err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if err = sp.store.Delete(path.kind, path.key, data.Version); err != nil {
					storeUpdateFailed("streaming deletion of "+path.key, err)
				}

			case indirectPatchEvent:
				path, err := parsePath(event.Data())
				if err != nil {
					gotMalformedEvent(event, err)
				}
				item, requestErr := sp.requestor.requestResource(path.kind, path.key)
				if requestErr != nil {
					sp.config.Loggers.Errorf(`Unexpected error requesting %s item "%s": %+v`, path.kind, path.key, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					storeUpdateFailed("streaming update of "+path.key, err)
				}
			default:
				sp.config.Loggers.Infof("Unexpected event found in stream: %s", event.Event())
			}

			if shouldRestart {
				stream.Restart()
			}

		case newStoreStatus := <-statusCh:
			if newStoreStatus.Available {
				// The store has just transitioned from unavailable to available (scenario 2a above)
				if newStoreStatus.NeedsRefresh {
					// The store is telling us that it can't guarantee that all of the latest data was cached.
					// So we'll restart the stream to ensure a full refresh.
					sp.config.Loggers.Warn("Restarting stream to refresh data after feature store outage")
					stream.Restart()
				}
				// All of the updates were cached and have been written to the store, so we don't need to
				// restart the stream. We just need to make sure the client knows we're initialized now
				// (in case the initial "put" was not stored).
				sp.setInitializedAndNotifyClient(true, closeWhenReady)
			}

		case <-sp.halt:
			stream.Close()
			return
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
	req, _ := http.NewRequest("GET", sp.config.StreamUri+"/all", nil)
	addBaseHeaders(req, sp.sdkKey, sp.config)
	sp.config.Loggers.Info("Connecting to LaunchDarkly stream")

	sp.logConnectionStarted()

	initialRetryDelay := sp.config.StreamInitialReconnectDelay
	if initialRetryDelay <= 0 {
		initialRetryDelay = defaultStreamRetryDelay
	}

	errorHandler := func(err error) es.StreamErrorHandlerResult {
		sp.logConnectionResult(false)
		shouldStreamShutDown := sp.checkIfPermanentFailure(err) // this also logs the error
		if !shouldStreamShutDown {
			sp.logConnectionStarted()
		}
		return es.StreamErrorHandlerResult{CloseNow: shouldStreamShutDown}
	}

	stream, err := es.SubscribeWithRequestAndOptions(req,
		es.StreamOptionHTTPClient(sp.client),
		es.StreamOptionReadTimeout(streamReadTimeout),
		es.StreamOptionInitialRetry(initialRetryDelay),
		es.StreamOptionUseBackoff(streamMaxRetryDelay),
		es.StreamOptionUseJitter(streamJitterRatio),
		es.StreamOptionRetryResetInterval(streamRetryResetInterval),
		es.StreamOptionErrorHandler(errorHandler),
		es.StreamOptionCanRetryFirstConnection(-1),
		es.StreamOptionLogger(sp.config.Loggers.ForLevel(ldlog.Info)),
	)

	if err != nil {
		sp.logConnectionResult(false)

		close(closeWhenReady)
		return
	}

	sp.consumeStream(stream, closeWhenReady)
}

func (sp *streamProcessor) setInitializedAndNotifyClient(success bool, closeWhenReady chan<- struct{}) {
	if success {
		sp.setInitializedOnce.Do(func() {
			sp.config.Loggers.Info("LaunchDarkly streaming is active")
			sp.isInitialized = true
		})
	}
	sp.readyOnce.Do(func() {
		close(closeWhenReady)
	})
}

func (sp *streamProcessor) checkIfPermanentFailure(err error) bool {
	if se, ok := err.(es.SubscriptionError); ok {
		sp.config.Loggers.Error(httpErrorMessage(se.Code, "streaming connection", "will retry"))
		return !isHTTPErrorRecoverable(se.Code)
	}
	sp.config.Loggers.Errorf("Network error on streaming connection: %s", err.Error())
	return false
}

func (sp *streamProcessor) logConnectionStarted() {
	sp.connectionAttemptLock.Lock()
	defer sp.connectionAttemptLock.Unlock()
	sp.connectionAttemptStartTime = now()
}

func (sp *streamProcessor) logConnectionResult(success bool) {
	sp.connectionAttemptLock.Lock()
	startTimeWas := sp.connectionAttemptStartTime
	sp.connectionAttemptStartTime = 0
	sp.connectionAttemptLock.Unlock()

	if startTimeWas > 0 && sp.config.diagnosticsManager != nil {
		timestamp := now()
		sp.config.diagnosticsManager.RecordStreamInit(timestamp, !success,
			milliseconds(timestamp-startTimeWas))
	}
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
