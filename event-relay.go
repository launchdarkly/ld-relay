package main

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

	ld "gopkg.in/launchdarkly/go-client.v3"
)

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
	eventSchemaHeader          = "X-LaunchDarkly-Event-Schema"
	summaryEventsSchemaVersion = 3
)

type relayHandler struct {
	config       Config
	sdkKey       string
	featureStore ld.FeatureStore

	verbatimRelay    *eventVerbatimRelay
	summarizingRelay *eventSummarizingRelay

	mu sync.Mutex
}

func (r *relayHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, bodyErr := ioutil.ReadAll(req.Body)
	if bodyErr != nil {
		Error.Printf("Error reading event post body: %+v", bodyErr)
	}
	var evts []json.RawMessage

	defer req.Body.Close()
	go func() {
		if err := recover(); err != nil {
			Error.Printf("Unexpected panic in event relay : %+v", err)
		}

		evts = make([]json.RawMessage, 0)
		err := json.Unmarshal(body, &evts)
		if err != nil {
			Error.Printf("Error unmarshaling event post body: %+v", err)
		}

		payloadVersion, _ := strconv.Atoi(req.Header.Get(eventSchemaHeader))
		switch payloadVersion {
		case summaryEventsSchemaVersion:
			// New-style events that have already gone through summarization - deliver them as-is
			r.getVerbatimRelay().enqueue(evts)
		default:
			r.getSummarizingRelay().enqueue(evts)
		}
	}()
}

func (r *relayHandler) getVerbatimRelay() *eventVerbatimRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbatimRelay == nil {
		r.verbatimRelay = newEventVerbatimRelay(r.sdkKey, r.config)
	}
	return r.verbatimRelay
}

func (r *relayHandler) getSummarizingRelay() *eventSummarizingRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay == nil {
		r.summarizingRelay = newEventSummarizingRelay(r.sdkKey, r.config, r.featureStore)
	}
	return r.summarizingRelay
}

// Create a new handler for serving a specified channel
func newRelayHandler(sdkKey string, config Config, featureStore ld.FeatureStore) *relayHandler {
	return &relayHandler{
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
			Error.Printf("Unexpected panic in event relay : %+v", err)
		}

		ticker := time.NewTicker(time.Duration(config.Events.FlushIntervalSecs) * time.Second)
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
	uri := er.config.Events.EventsUri + "/bulk"
	er.mu.Lock()
	if len(er.queue) == 0 {
		er.mu.Unlock()
		return
	}

	events := er.queue
	er.mu.Unlock()
	er.queue = make([]json.RawMessage, 0)

	payload, _ := json.Marshal(events)

	req, reqErr := http.NewRequest("POST", uri, bytes.NewReader(payload))

	if reqErr != nil {
		Error.Printf("Unexpected error while creating event request: %+v", reqErr)
	}

	req.Header.Add("Authorization", er.sdkKey)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("User-Agent", "LDRelay/"+VERSION)
	req.Header.Add(eventSchemaHeader, strconv.Itoa(summaryEventsSchemaVersion))

	resp, respErr := er.client.Do(req)

	defer func() {
		if resp != nil && resp.Body != nil {
			ioutil.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}()

	if respErr != nil {
		Error.Printf("Unexpected error while sending events: %+v", respErr)
		return
	}
	err := checkStatusCode(resp.StatusCode, uri)
	if err != nil {
		Error.Printf("Unexpected status code when sending events: %+v", respErr)
	}
}

func (er *eventVerbatimRelay) enqueue(evts []json.RawMessage) {
	if !er.config.Events.SendEvents {
		return
	}

	if er.config.Events.SamplingInterval > 0 && rGen.Int31n(er.config.Events.SamplingInterval) != 0 {
		return
	}

	if len(er.queue) >= er.config.Events.Capacity {
		Warning.Println("Exceeded event queue capacity. Increase capacity to avoid dropping events.")
	} else {
		er.queue = append(er.queue, evts...)
	}
}

func checkStatusCode(statusCode int, url string) error {
	if statusCode == http.StatusUnauthorized {
		return fmt.Errorf("Invalid SDK key when accessing URL: %s. Verify that your SDK key is correct.", url)
	}

	if statusCode == http.StatusNotFound {
		return fmt.Errorf("Resource not found when accessing URL: %s. Verify that this resource exists.", url)
	}

	if statusCode/100 != 2 {
		return fmt.Errorf("Unexpected response code: %d when accessing URL: %s", statusCode, url)
	}
	return nil
}
