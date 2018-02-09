package ldclient

import (
	"encoding/json"
	es "github.com/launchdarkly/eventsource"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	putEvent           = "put"
	patchEvent         = "patch"
	deleteEvent        = "delete"
	indirectPutEvent   = "indirect/put"
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
	sync.RWMutex
}

type featurePatchData struct {
	Path string      `json:"path"`
	Data FeatureFlag `json:"data"`
}

type featureDeleteData struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

func (sp *streamProcessor) initialized() bool {
	return sp.isInitialized
}

func (sp *streamProcessor) start(ch chan<- bool) {
	sp.config.Logger.Printf("Starting LaunchDarkly streaming connection")
	go sp.startOnce(ch)
	go sp.errors()
}

func (sp *streamProcessor) startOnce(ch chan<- bool) {
	for {
		subscribed := sp.checkSubscribe()
		if !subscribed {
			time.Sleep(2 * time.Second)
			continue
		}
		event := <-sp.stream.Events
		switch event.Event() {
		case putEvent:
			var features map[string]*FeatureFlag
			if err := json.Unmarshal([]byte(event.Data()), &features); err != nil {
				sp.config.Logger.Printf("Unexpected error unmarshalling feature json: %+v", err)
			} else {
				sp.store.Init(features)
				sp.setInitializedOnce.Do(func() {
					sp.config.Logger.Printf("Started LaunchDarkly streaming client")
					sp.isInitialized = true
					ch <- true
				})
			}
		case patchEvent:
			var patch featurePatchData
			if err := json.Unmarshal([]byte(event.Data()), &patch); err != nil {
				sp.config.Logger.Printf("Unexpected error unmarshalling feature patch json: %+v", err)
			} else {
				key := strings.TrimLeft(patch.Path, "/")
				sp.store.Upsert(key, patch.Data)
			}
		case indirectPatchEvent:
			key := event.Data()
			if feature, err := sp.requestor.requestFlag(key); err != nil {
				sp.config.Logger.Printf("Unexpected error requesting feature: %+v", err)
			} else {
				sp.store.Upsert(key, *feature)
			}
		case indirectPutEvent:
			if features, _, err := sp.requestor.requestAllFlags(); err != nil {
				sp.config.Logger.Printf("Unexpected error requesting all features: %+v", err)
			} else {
				sp.store.Init(features)
				sp.setInitializedOnce.Do(func() {
					sp.config.Logger.Printf("Started LaunchDarkly streaming client")
					sp.isInitialized = true
					ch <- true
				})
			}
		case deleteEvent:
			var data featureDeleteData
			if err := json.Unmarshal([]byte(event.Data()), &data); err != nil {
				sp.config.Logger.Printf("Unexpected error unmarshalling feature delete json: %+v", err)
			} else {
				key := strings.TrimLeft(data.Path, "/")
				sp.store.Delete(key, data.Version)
			}
		default:
			sp.config.Logger.Printf("Unexpected event found in stream: %s", event.Event())
		}
	}
}

func newStreamProcessor(sdkKey string, config Config, requestor *requestor) updateProcessor {
	sp := &streamProcessor{
		store:     config.FeatureStore,
		config:    config,
		sdkKey:    sdkKey,
		requestor: requestor,
	}

	return sp
}

func (sp *streamProcessor) subscribe() {
	sp.Lock()
	defer sp.Unlock()

	if sp.stream == nil {
		req, _ := http.NewRequest("GET", sp.config.StreamUri+"/flags", nil)
		req.Header.Add("Authorization", sp.sdkKey)
		req.Header.Add("User-Agent", "GoClient/"+Version)
		sp.config.Logger.Printf("Connecting to LaunchDarkly stream using URL: %s", req.URL.String())

		if stream, err := es.SubscribeWithRequest("", req); err != nil {
			sp.config.Logger.Printf("Error subscribing to stream: %+v using URL: %s", err, req.URL.String())
		} else {
			sp.stream = stream
			sp.stream.Logger = sp.config.Logger
		}
	}
}

func (sp *streamProcessor) checkSubscribe() bool {
	sp.RLock()
	if sp.stream == nil {
		sp.RUnlock()
		sp.subscribe()
		return sp.stream != nil
	} else {
		defer sp.RUnlock()
		return true
	}
}

func (sp *streamProcessor) errors() {
	for {
		subscribed := sp.checkSubscribe()
		if !subscribed {
			time.Sleep(2 * time.Second)
			continue
		}
		err := <-sp.stream.Errors

		if err != io.EOF {
			sp.config.Logger.Printf("Error encountered processing stream: %+v", err)
		}
		if err != nil {
		}
	}
}

func (sp *streamProcessor) close() {
	// TODO : the EventSource library doesn't support close() yet.
	// when it does, call it here
}
