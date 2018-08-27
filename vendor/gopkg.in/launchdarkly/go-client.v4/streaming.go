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
)

const (
	putEvent           = "put"
	patchEvent         = "patch"
	deleteEvent        = "delete"
	indirectPatchEvent = "indirect/patch"
)

type streamProcessor struct {
	store              FeatureStore
	requestor          *requestor
	stream             *es.Stream
	config             Config
	sdkKey             string
	setInitializedOnce sync.Once
	isInitialized      bool
	halt               chan struct{}
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
	sp.config.Logger.Printf("Starting LaunchDarkly streaming connection")
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

func (sp *streamProcessor) events(closeWhenReady chan<- struct{}) {
	var readyOnce sync.Once
	notifyReady := func() {
		readyOnce.Do(func() {
			close(closeWhenReady)
		})
	}
	// Ensure we stop waiting for initialization if we exit, even if initialization fails
	defer notifyReady()

	// Consume remaining Events and Errors so we can garbage collect
	defer func() {
		for range sp.stream.Events {
		}
		for range sp.stream.Errors {
		}
	}()

	for {
		select {
		case event, ok := <-sp.stream.Events:
			if !ok {
				sp.config.Logger.Printf("Event stream closed.")
				return
			}
			switch event.Event() {
			case putEvent:
				var put putData
				if err := json.Unmarshal([]byte(event.Data()), &put); err != nil {
					sp.config.Logger.Printf("ERROR: Unexpected error unmarshalling PUT json: %+v", err)
					break
				}
				err := sp.store.Init(MakeAllVersionedDataMap(put.Data.Flags, put.Data.Segments))
				if err != nil {
					sp.config.Logger.Printf("Error initializing store: %s", err)
					return
				}
				sp.setInitializedOnce.Do(func() {
					sp.config.Logger.Printf("Started LaunchDarkly streaming client")
					sp.isInitialized = true
					notifyReady()
				})
			case patchEvent:
				var patch patchData
				if err := json.Unmarshal([]byte(event.Data()), &patch); err != nil {
					sp.config.Logger.Printf("ERROR: Unexpected error unmarshalling PATCH json: %+v", err)
					break
				}
				path, err := parsePath(patch.Path)
				if err != nil {
					sp.config.Logger.Printf("ERROR: Unable to process event %s: %s", event.Event(), err)
					break
				}
				item := path.kind.GetDefaultItem().(VersionedData)
				if err = json.Unmarshal(patch.Data, item); err != nil {
					sp.config.Logger.Printf("ERROR: Unexpected error unmarshalling json for %s item: %+v", path.kind, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					sp.config.Logger.Printf("ERROR: Unexpected error storing segment json: %+v", err)
				}
			case deleteEvent:
				var data deleteData
				if err := json.Unmarshal([]byte(event.Data()), &data); err != nil {
					sp.config.Logger.Printf("ERROR: Unexpected error unmarshalling DELETE json: %+v", err)
					break
				}
				path, err := parsePath(data.Path)
				if err != nil {
					sp.config.Logger.Printf("ERROR: Unable to process event %s: %s", event.Event(), err)
					break
				}
				if err = sp.store.Delete(path.kind, path.key, data.Version); err != nil {
					sp.config.Logger.Printf(`ERROR: Unexpected error deleting %s object "%s": %s`, path.kind, path.key, err)
				}
			case indirectPatchEvent:
				path, err := parsePath(event.Data())
				if err != nil {
					sp.config.Logger.Printf("ERROR: Unable to process event %s: %s", event.Event(), err)
					break
				}
				item, requestErr := sp.requestor.requestResource(path.kind, path.key)
				if requestErr != nil {
					sp.config.Logger.Printf(`ERROR: Unexpected error requesting %s item "%s": %+v`, path.kind, path.key, err)
					break
				}
				if err = sp.store.Upsert(path.kind, item); err != nil {
					sp.config.Logger.Printf(`ERROR: Unexpected error store %s item "%s": %+v`, path.kind, path.key, err)
				}
			default:
				sp.config.Logger.Printf("Unexpected event found in stream: %s", event.Event())
			}
		case err, ok := <-sp.stream.Errors:
			if !ok {
				sp.config.Logger.Printf("Event error stream closed.")
				return // Otherwise we will spin in this loop
			}
			if err != io.EOF {
				sp.config.Logger.Printf("ERROR: Error encountered processing stream: %+v", err)
				if sp.checkIfPermanentFailure(err) {
					sp.closeOnce.Do(func() {
						sp.config.Logger.Printf("Closing event stream.")
						sp.stream.Close()
					})
					return
				}
			}
		case <-sp.halt:
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

	return sp
}

func (sp *streamProcessor) subscribe(closeWhenReady chan<- struct{}) {
	for {
		req, _ := http.NewRequest("GET", sp.config.StreamUri+"/all", nil)
		req.Header.Add("Authorization", sp.sdkKey)
		req.Header.Add("User-Agent", sp.config.UserAgent)
		sp.config.Logger.Printf("Connecting to LaunchDarkly stream using URL: %s", req.URL.String())

		if stream, err := es.SubscribeWithRequest("", req); err != nil {
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
			sp.stream = stream
			sp.stream.Logger = sp.config.Logger

			go sp.events(closeWhenReady)
			return
		}
	}
}

func (sp *streamProcessor) checkIfPermanentFailure(err error) bool {
	if se, ok := err.(es.SubscriptionError); ok {
		sp.config.Logger.Printf("ERROR: %s", httpErrorMessage(se.Code, "streaming connection", "will retry"))
		if !isHTTPErrorRecoverable(se.Code) {
			return true
		}
	}
	return false
}

// Close instructs the processor to stop receiving updates
func (sp *streamProcessor) Close() error {
	sp.closeOnce.Do(func() {
		sp.config.Logger.Printf("Closing event stream.")
		if sp.stream != nil {
			sp.stream.Close()
		}
		close(sp.halt)
	})
	return nil
}
