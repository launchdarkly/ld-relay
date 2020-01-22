package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"

	"gopkg.in/launchdarkly/ld-relay.v5/httpconfig"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/util"
)

// EventRelay configuration - used in the config file struct in relay.go
type Config struct {
	EventsUri         string
	SendEvents        bool
	FlushIntervalSecs int
	SamplingInterval  int32
	Capacity          int
	InlineUsers       bool
}

// Describes one of the possible endpoints (on both events.launchdarkly.com and the relay) for posting events
type Endpoint interface {
	fmt.Stringer
}

type (
	serverSDKEventsEndpoint            struct{}
	mobileSDKEventsEndpoint            struct{}
	javaScriptSDKEventsEndpoint        struct{}
	serverDiagnosticEventsEndpoint     struct{}
	mobileDiagnosticEventsEndpoint     struct{}
	javaScriptDiagnosticEventsEndpoint struct{}
)

var (
	ServerSDKEventsEndpoint               = &serverSDKEventsEndpoint{}
	MobileSDKEventsEndpoint               = &mobileSDKEventsEndpoint{}
	JavaScriptSDKEventsEndpoint           = &javaScriptSDKEventsEndpoint{}
	ServerSDKDiagnosticEventsEndpoint     = &serverDiagnosticEventsEndpoint{}
	MobileSDKDiagnosticEventsEndpoint     = &mobileDiagnosticEventsEndpoint{}
	JavaScriptSDKDiagnosticEventsEndpoint = &javaScriptDiagnosticEventsEndpoint{}
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
	endpoints map[Endpoint]eventEndpointDispatcher
}

type eventEndpointDispatcher interface {
	dispatch(w http.ResponseWriter, req *http.Request)
}

type analyticsEventEndpointDispatcher struct {
	config           Config
	httpClient       *http.Client
	httpConfig       httpconfig.HTTPConfig
	authKey          string
	remotePath       string
	verbatimRelay    *eventVerbatimRelay
	summarizingRelay *eventSummarizingRelay
	featureStore     ld.FeatureStore
	loggers          ldlog.Loggers
	mu               sync.Mutex
}

type diagnosticEventEndpointDispatcher struct {
	httpClient        *http.Client
	remoteEndpointURI string
	loggers           ldlog.Loggers
}

func (e *serverSDKEventsEndpoint) String() string {
	return "ServerSDKEventsEndpoint"
}

func (e *mobileSDKEventsEndpoint) String() string {
	return "MobileSDKEventsEndpoint"
}

func (e *javaScriptSDKEventsEndpoint) String() string {
	return "JavaScriptSDKEventsEndpoint"
}

func (e *serverDiagnosticEventsEndpoint) String() string {
	return "ServerDiagnosticEventsEndpoint"
}

func (e *mobileDiagnosticEventsEndpoint) String() string {
	return "MobileDiagnosticEventsEndpoint"
}

func (e *javaScriptDiagnosticEventsEndpoint) String() string {
	return "JavaScriptDiagnosticEventsEndpoint"
}

func (r *EventDispatcher) GetHandler(endpoint Endpoint) func(w http.ResponseWriter, req *http.Request) {
	d := r.endpoints[endpoint]
	if d != nil {
		return d.dispatch
	}
	return nil
}

func (r *analyticsEventEndpointDispatcher) dispatch(w http.ResponseWriter, req *http.Request) {
	consumeEvents(w, req, r.loggers, func(body []byte) {
		evts := make([]json.RawMessage, 0)
		err := json.Unmarshal(body, &evts)
		if err != nil {
			r.loggers.Errorf("Error unmarshaling event post body: %+v", err)
			return
		}

		payloadVersion, _ := strconv.Atoi(req.Header.Get(EventSchemaHeader))
		if payloadVersion == 0 {
			payloadVersion = 1
		}
		r.loggers.Debugf("Received %d events (v%d) to be proxied to %s", len(evts), payloadVersion, r.remotePath)
		if payloadVersion >= SummaryEventsSchemaVersion {
			// New-style events that have already gone through summarization - deliver them as-is
			r.getVerbatimRelay().enqueue(evts)
		} else {
			r.getSummarizingRelay().enqueue(evts, payloadVersion)
		}
	})
}

func (d *diagnosticEventEndpointDispatcher) dispatch(w http.ResponseWriter, req *http.Request) {
	consumeEvents(w, req, d.loggers, func(body []byte) {
		// We are just operating as a reverse proxy and passing the request on verbatim to LD; we do not
		// need to parse the JSON.
		d.loggers.Debugf("Received diagnostic event to be proxied to %s", d.remoteEndpointURI)

		for attempt := 0; attempt < 2; attempt++ { // use the same retry logic that the SDK uses
			if attempt > 0 {
				d.loggers.Warn("Will retry posting diagnostic event after 1 second")
				time.Sleep(1 * time.Second)
			}

			forwardReq, reqErr := http.NewRequest("POST", d.remoteEndpointURI, bytes.NewReader(body))
			if reqErr != nil {
				d.loggers.Errorf("Unexpected error while creating event request: %+v", reqErr)
				return
			}
			forwardReq.Header.Add("Content-Type", "application/json")

			// Copy the Authorization header, if any (used only for server-side and mobile); also copy User-Agent
			if authKey := req.Header.Get("Authorization"); authKey != "" {
				forwardReq.Header.Add("Authorization", authKey)
			}
			if userAgent := req.Header.Get("User-Agent"); userAgent != "" {
				forwardReq.Header.Add("User-Agent", userAgent)
			}
			// diagnostic events do not have schema or payload ID headers

			resp, respErr := d.httpClient.Do(forwardReq)
			if resp != nil && resp.Body != nil {
				_, _ = ioutil.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			if respErr != nil {
				d.loggers.Warnf("Unexpected error while sending events: %+v", respErr)
			} else if resp.StatusCode >= 400 {
				d.loggers.Warnf("Received error status %d when sending events", resp.StatusCode)
			} else {
				break
			}
		}
	})
}

func consumeEvents(w http.ResponseWriter, req *http.Request, loggers ldlog.Loggers, thenExecute func([]byte)) {
	body, bodyErr := ioutil.ReadAll(req.Body)

	if bodyErr != nil {
		loggers.Errorf("Error reading event post body: %+v", bodyErr)
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
				loggers.Errorf("Unexpected panic in event relay: %+v", err)
			}
		}()
		thenExecute(body)
	}()
}

func (r *analyticsEventEndpointDispatcher) getVerbatimRelay() *eventVerbatimRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbatimRelay == nil {
		r.verbatimRelay = newEventVerbatimRelay(r.authKey, r.config, r.httpClient, r.loggers, r.remotePath)
	}
	return r.verbatimRelay
}

func (r *analyticsEventEndpointDispatcher) getSummarizingRelay() *eventSummarizingRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay == nil {
		r.summarizingRelay = newEventSummarizingRelay(r.authKey, r.config, r.httpConfig, r.featureStore, r.loggers, r.remotePath)
	}
	return r.summarizingRelay
}

// NewEventDispatcher creates a handler for relaying events to LaunchDarkly for an environment
func NewEventDispatcher(sdkKey string, mobileKey *string, envID *string, loggers ldlog.Loggers, config Config, httpConfig httpconfig.HTTPConfig, featureStore ld.FeatureStore) *EventDispatcher {
	httpClient := httpConfig.Client()
	ep := &EventDispatcher{
		endpoints: map[Endpoint]eventEndpointDispatcher{
			ServerSDKEventsEndpoint: newAnalyticsEventEndpointDispatcher(sdkKey, config, httpConfig, httpClient, featureStore, loggers, "/bulk"),
		},
	}
	ep.endpoints[ServerSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers, "/diagnostic")
	if mobileKey != nil {
		ep.endpoints[MobileSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher(*mobileKey, config, httpConfig, httpClient, featureStore, loggers, "/mobile")
		ep.endpoints[MobileSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers, "/mobile/events/diagnostic")
	}
	if envID != nil {
		ep.endpoints[JavaScriptSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher("", config, httpConfig, httpClient, featureStore, loggers, "/events/bulk/"+*envID)
		ep.endpoints[JavaScriptSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers, "/events/diagnostic/"+*envID)
	}
	return ep
}

func newDiagnosticEventEndpointDispatcher(config Config, httpClient *http.Client, loggers ldlog.Loggers, remotePath string) *diagnosticEventEndpointDispatcher {
	return &diagnosticEventEndpointDispatcher{
		httpClient:        httpClient,
		remoteEndpointURI: strings.TrimRight(config.EventsUri, "/") + remotePath,
		loggers:           loggers,
	}
}

func newAnalyticsEventEndpointDispatcher(authKey string, config Config, httpConfig httpconfig.HTTPConfig,
	httpClient *http.Client, featureStore ld.FeatureStore, loggers ldlog.Loggers, remotePath string) *analyticsEventEndpointDispatcher {
	return &analyticsEventEndpointDispatcher{
		authKey:      authKey,
		config:       config,
		httpConfig:   httpConfig,
		httpClient:   httpClient,
		featureStore: featureStore,
		loggers:      loggers,
		remotePath:   remotePath,
	}
}

func newEventVerbatimRelay(sdkKey string, config Config, httpClient *http.Client, loggers ldlog.Loggers, remotePath string) *eventVerbatimRelay {
	opts := []OptionType{
		OptionCapacity(config.Capacity),
		OptionEndpointURI(strings.TrimRight(config.EventsUri, "/") + remotePath),
		OptionClient{Client: httpClient},
	}

	if config.FlushIntervalSecs > 0 {
		opts = append(opts, OptionFlushInterval(time.Duration(config.FlushIntervalSecs)*time.Second))
	}

	publisher, _ := NewHttpEventPublisher(sdkKey, loggers, opts...)

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
