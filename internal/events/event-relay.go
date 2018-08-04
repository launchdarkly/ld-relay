package events

import (
	"encoding/json"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	ld "gopkg.in/launchdarkly/go-client.v4"

	"gopkg.in/launchdarkly/ld-relay.v5/internal/util"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
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
	config    Config
	publisher EventPublisher
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
	opts := []OptionType{
		OptionCapacity(config.Capacity),
		OptionUri(config.EventsUri),
	}

	if config.FlushIntervalSecs > 0 {
		opts = append(opts, OptionFlushInterval(time.Duration(config.FlushIntervalSecs)*time.Second))
	}

	publisher, _ := NewHttpEventPublisher(sdkKey, opts...)

	res := &eventVerbatimRelay{
		config:    config,
		publisher: publisher,
	}

	return res
}

func (er *eventVerbatimRelay) enqueue(evts []json.RawMessage) {
	if !er.config.SendEvents {
		return
	}

	if er.config.SamplingInterval > 0 && rGen.Int31n(er.config.SamplingInterval) != 0 {
		return
	}

	er.publisher.PublishRaw(evts...)
}
