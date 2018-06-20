package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	ld "gopkg.in/launchdarkly/go-client.v4"

	"gopkg.in/launchdarkly/ld-relay.v5/logging"
	"gopkg.in/launchdarkly/ld-relay.v5/util"
	"gopkg.in/launchdarkly/ld-relay.v5/version"
)

// EventRelay configuration
type Config struct {
	EventsUri         string
	SendEvents        bool
	FlushIntervalSecs int
	SamplingInterval  int32
	Capacity          int
	InlineUsers       bool
}

type eventVerbatimRelay struct {
	sdkKey string
	config Config
	mu     *sync.Mutex
	client *http.Client
	closer chan struct{}
	queue  []json.RawMessage
}

var rGen *rand.Rand

func init() {
	s1 := rand.NewSource(time.Now().UnixNano())
	rGen = rand.New(s1)
}

const (
	// SummaryEventsSchemaVersion is the minimum event schema that supports summary events
	SummaryEventsSchemaVersion = 3

	// EventSchemaHeader is an HTTP header that describes the schema version for event requests
	EventSchemaHeader = "X-LaunchDarkly-Event-Schema"
)

// EventRelayHandler is a handler for relaying events to LaunchDarkly for an environment
type EventRelayHandler struct {
	config       Config
	sdkKey       string
	featureStore ld.FeatureStore

	verbatimRelay    *eventVerbatimRelay
	summarizingRelay *eventSummarizingRelay

	mu sync.Mutex
}

func (r *EventRelayHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, bodyErr := ioutil.ReadAll(req.Body)

	if bodyErr != nil {
		logging.Error.Printf("Error reading event post body: %+v", bodyErr)
		w.WriteHeader(http.StatusBadRequest)
		w.Write(util.ErrorJsonMsg("unable to read request body"))
		return
	}

	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(util.ErrorJsonMsg("body may not be empty"))
		return
	}

	// Always accept the data
	w.WriteHeader(http.StatusAccepted)

	go func() {
		defer func() {
			if err := recover(); err != nil {
				logging.Error.Printf("Unexpected panic in event relay : %+v", err)
			}
		}()

		evts := make([]json.RawMessage, 0)
		err := json.Unmarshal(body, &evts)
		if err != nil {
			logging.Error.Printf("Error unmarshaling event post body: %+v", err)
			return
		}

		payloadVersion, _ := strconv.Atoi(req.Header.Get(EventSchemaHeader))
		if payloadVersion == 0 {
			payloadVersion = 1
		}
		if payloadVersion >= SummaryEventsSchemaVersion {
			// New-style events that have already gone through summarization - deliver them as-is
			r.getVerbatimRelay().enqueue(evts)
		} else {
			r.getSummarizingRelay().enqueue(evts, payloadVersion)
		}
	}()
}

func (r *EventRelayHandler) getVerbatimRelay() *eventVerbatimRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbatimRelay == nil {
		r.verbatimRelay = newEventVerbatimRelay(r.sdkKey, r.config)
	}
	return r.verbatimRelay
}

func (r *EventRelayHandler) getSummarizingRelay() *eventSummarizingRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay == nil {
		r.summarizingRelay = newEventSummarizingRelay(r.sdkKey, r.config, r.featureStore)
	}
	return r.summarizingRelay
}

// NewEventRelayHandler create a handler for relaying events to LaunchDarkly for an environment
func NewEventRelayHandler(sdkKey string, config Config, featureStore ld.FeatureStore) *EventRelayHandler {
	return &EventRelayHandler{
		sdkKey:       sdkKey,
		config:       config,
		featureStore: featureStore,
	}
}

func newEventVerbatimRelay(sdkKey string, config Config) *eventVerbatimRelay {
	res := &eventVerbatimRelay{
		queue:  make([]json.RawMessage, 0),
		sdkKey: sdkKey,
		config: config,
		client: &http.Client{},
		closer: make(chan struct{}),
		mu:     &sync.Mutex{},
	}

	go func() {
		if err := recover(); err != nil {
			logging.Error.Printf("Unexpected panic in event relay : %+v", err)
		}

		ticker := time.NewTicker(time.Duration(config.FlushIntervalSecs) * time.Second)
		for {
			select {
			case <-ticker.C:
				res.flush()
			case <-res.closer:
				ticker.Stop()
				return
			}
		}
	}()

	return res
}

func (er *eventVerbatimRelay) flush() {
	uri := er.config.EventsUri + "/bulk"
	er.mu.Lock()
	if len(er.queue) == 0 {
		er.mu.Unlock()
		return
	}

	events := er.queue
	er.queue = make([]json.RawMessage, 0)
	er.mu.Unlock()

	payload, _ := json.Marshal(events)

	req, reqErr := http.NewRequest("POST", uri, bytes.NewReader(payload))

	if reqErr != nil {
		logging.Error.Printf("Unexpected error while creating event request: %+v", reqErr)
	}

	req.Header.Add("Authorization", er.sdkKey)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("User-Agent", "LDRelay/"+version.Version)
	req.Header.Add(EventSchemaHeader, strconv.Itoa(SummaryEventsSchemaVersion))

	resp, respErr := er.client.Do(req)

	defer func() {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close() // nolint:errcheck
			_, _ = ioutil.ReadAll(resp.Body)
		}
	}()

	if respErr != nil {
		logging.Error.Printf("Unexpected error while sending events: %+v", respErr)
		return
	}
	err := checkStatusCode(resp.StatusCode, uri)
	if err != nil {
		logging.Error.Printf("Unexpected status code when sending events: %+v", respErr)
	}
}

func (er *eventVerbatimRelay) enqueue(evts []json.RawMessage) {
	if !er.config.SendEvents {
		return
	}

	if er.config.SamplingInterval > 0 && rGen.Int31n(er.config.SamplingInterval) != 0 {
		return
	}

	er.mu.Lock()
	defer er.mu.Unlock()

	if len(er.queue) >= er.config.Capacity {
		logging.Warning.Println("Exceeded event queue capacity. Increase capacity to avoid dropping events.")
	} else {
		er.queue = append(er.queue, evts...)
	}
}

func checkStatusCode(statusCode int, url string) error {
	if statusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid SDK key when accessing URL: %s. Verify that your SDK key is correct", url)
	}

	if statusCode == http.StatusNotFound {
		return fmt.Errorf("resource not found when accessing URL: %s. Verify that this resource exists", url)
	}

	if statusCode/100 != 2 {
		return fmt.Errorf("unexpected response code: %d when accessing URL: %s", statusCode, url)
	}
	return nil
}
