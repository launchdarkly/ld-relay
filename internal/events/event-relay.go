package events

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"path"
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

// Describes one of the possible endpoints (on both events.launchdarkly.com and the relay) for posting events
type EventsEndpoint interface {
	fmt.Stringer
	remoteUrlPath(config Config) string
}

type (
	serverSDKEventsEndpoint     struct{}
	mobileSDKEventsEndpoint     struct{}
	javaScriptSDKEventsEndpoint struct{}
)

var (
	ServerSDKEventsEndpoint     = &serverSDKEventsEndpoint{}
	MobileSDKEventsEndpoint     = &mobileSDKEventsEndpoint{}
	JavaScriptSDKEventsEndpoint = &javaScriptSDKEventsEndpoint{}
)

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

// EventDispatcher relays events to LaunchDarkly for an environment
type EventDispatcher struct {
	endpoints map[EventsEndpoint]*eventEndpointDispatcher
}

type eventEndpointDispatcher struct {
	config           Config
	authKey          string
	remotePath       string
	verbatimRelay    *eventVerbatimRelay
	summarizingRelay *eventSummarizingRelay
	featureStore     ld.FeatureStore
	mu               sync.Mutex
}

func (e *serverSDKEventsEndpoint) String() string {
	return "ServerSDKEventsEndpoint"
}

func (e *serverSDKEventsEndpoint) remoteUrlPath(config Config) string {
	return "/bulk"
}

func (e *mobileSDKEventsEndpoint) String() string {
	return "MobileSDKEventsEndpoint"
}

func (e *mobileSDKEventsEndpoint) remoteUrlPath(config Config) string {
	return "/mobile/events/bulk"
}

func (e *javaScriptSDKEventsEndpoint) String() string {
	return "JavaScriptSDKEventsEndpoint"
}

func (e javaScriptSDKEventsEndpoint) remoteUrlPath(config Config) string {
	return "/events/bulk/" + "X" // TODO
}

func (r *EventDispatcher) GetHandler(endpoint EventsEndpoint) func(w http.ResponseWriter, req *http.Request) {
	d := r.endpoints[endpoint]
	if d != nil {
		return d.dispatchEvents
	}
	return nil
}

func (r *eventEndpointDispatcher) dispatchEvents(w http.ResponseWriter, req *http.Request) {
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

func (r *eventEndpointDispatcher) getVerbatimRelay() *eventVerbatimRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbatimRelay == nil {
		r.verbatimRelay = newEventVerbatimRelay(r.authKey, r.config, r.remotePath)
	}
	return r.verbatimRelay
}

func (r *eventEndpointDispatcher) getSummarizingRelay() *eventSummarizingRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay == nil {
		r.summarizingRelay = newEventSummarizingRelay(r.authKey, r.config, r.featureStore, r.remotePath)
	}
	return r.summarizingRelay
}

// NewEventDispatcher creates a handler for relaying events to LaunchDarkly for an environment
func NewEventDispatcher(sdkKey string, mobileKey *string, envID *string, config Config, featureStore ld.FeatureStore) *EventDispatcher {
	ep := &EventDispatcher{
		endpoints: map[EventsEndpoint]*eventEndpointDispatcher{
			ServerSDKEventsEndpoint: newEventEndpointDispatcher(sdkKey, config, featureStore, "/bulk"),
		},
	}
	if mobileKey != nil {
		ep.endpoints[MobileSDKEventsEndpoint] = newEventEndpointDispatcher(*mobileKey, config, featureStore, "/mobile")
	}
	if envID != nil {
		ep.endpoints[JavaScriptSDKEventsEndpoint] = newEventEndpointDispatcher("", config, featureStore, "/events/bulk/"+*envID)
	}
	return ep
}

func newEventEndpointDispatcher(authKey string, config Config, featureStore ld.FeatureStore, remotePath string) *eventEndpointDispatcher {
	return &eventEndpointDispatcher{
		authKey:      authKey,
		config:       config,
		featureStore: featureStore,
		remotePath:   remotePath,
	}
}

func newEventVerbatimRelay(sdkKey string, config Config, remotePath string) *eventVerbatimRelay {
	opts := []OptionType{
		OptionCapacity(config.Capacity),
		OptionEndpointURI(path.Join(config.EventsUri, remotePath)),
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
