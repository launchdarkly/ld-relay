package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/core/internal/util"
)

// Endpoint describes one of the possible endpoints (on both events.launchdarkly.com and Relay) for posting events.
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
	// ServerSDKEventsEndpoint describes the endpoint for server-side SDK analytics events.
	ServerSDKEventsEndpoint = &serverSDKEventsEndpoint{} //nolint:gochecknoglobals

	// MobileSDKEventsEndpoint describes the endpoint for mobile SDK analytics events.
	MobileSDKEventsEndpoint = &mobileSDKEventsEndpoint{} //nolint:gochecknoglobals

	// JavaScriptSDKEventsEndpoint describes the endpoint for JS/browser SDK analytics events.
	JavaScriptSDKEventsEndpoint = &javaScriptSDKEventsEndpoint{} //nolint:gochecknoglobals

	// ServerSDKDiagnosticEventsEndpoint describes the endpoint for server-side SDK diagnostic events.
	ServerSDKDiagnosticEventsEndpoint = &serverDiagnosticEventsEndpoint{} //nolint:gochecknoglobals

	// MobileSDKDiagnosticEventsEndpoint describes the endpoint for mobile SDK diagnostic events.
	MobileSDKDiagnosticEventsEndpoint = &mobileDiagnosticEventsEndpoint{} //nolint:gochecknoglobals

	// JavaScriptSDKDiagnosticEventsEndpoint describes the endpoint for JS/browser SDK diagnostic events.
	JavaScriptSDKDiagnosticEventsEndpoint = &javaScriptDiagnosticEventsEndpoint{} //nolint:gochecknoglobals
)

type eventVerbatimRelay struct {
	config    c.EventsConfig
	publisher EventPublisher
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
	config           c.EventsConfig
	httpClient       *http.Client
	httpConfig       httpconfig.HTTPConfig
	authKey          c.SDKCredential
	remotePath       string
	verbatimRelay    *eventVerbatimRelay
	summarizingRelay *eventSummarizingRelay
	storeAdapter     *store.SSERelayDataStoreAdapter
	loggers          ldlog.Loggers
	mu               sync.Mutex
}

type diagnosticEventEndpointDispatcher struct {
	httpClient        *http.Client
	httpConfig        httpconfig.HTTPConfig
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

// GetHandler returns the HTTP handler for an endpoint.
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
			switch {
			case respErr != nil:
				d.loggers.Warnf("Unexpected error while sending events: %+v", respErr)
			case resp.StatusCode >= 400:
				d.loggers.Warnf("Received error status %d when sending events", resp.StatusCode)
			default:
				return
			}
		}
	})
}

func consumeEvents(w http.ResponseWriter, req *http.Request, loggers ldlog.Loggers, thenExecute func([]byte)) {
	body, bodyErr := ioutil.ReadAll(req.Body)

	if bodyErr != nil {
		loggers.Errorf("Error reading event post body: %+v", bodyErr)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("unable to read request body"))
		return
	}

	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("body may not be empty"))
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
		r.verbatimRelay = newEventVerbatimRelay(r.authKey, r.config, r.httpConfig, r.loggers, r.remotePath)
	}
	return r.verbatimRelay
}

func (r *analyticsEventEndpointDispatcher) getSummarizingRelay() *eventSummarizingRelay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay == nil {
		r.summarizingRelay = newEventSummarizingRelay(r.config, r.httpConfig, r.storeAdapter, r.loggers, r.remotePath)
	}
	return r.summarizingRelay
}

// NewEventDispatcher creates a handler for relaying events to LaunchDarkly for an environment
func NewEventDispatcher(
	sdkKey c.SDKKey, //nolint:interfacer // we want to enforce that this parameter is an SDK key, not just an SDKCredential
	mobileKey c.MobileKey,
	envID c.EnvironmentID,
	loggers ldlog.Loggers,
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	storeAdapter *store.SSERelayDataStoreAdapter,
) *EventDispatcher {
	ep := &EventDispatcher{
		endpoints: map[Endpoint]eventEndpointDispatcher{
			ServerSDKEventsEndpoint: newAnalyticsEventEndpointDispatcher(sdkKey,
				config, httpConfig, storeAdapter, loggers, "/bulk"),
		},
	}
	ep.endpoints[ServerSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers, "/diagnostic")
	if mobileKey != "" {
		ep.endpoints[MobileSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher(mobileKey,
			config, httpConfig, storeAdapter, loggers, "/mobile")
		ep.endpoints[MobileSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers, "/mobile/events/diagnostic")
	}
	if envID != "" {
		ep.endpoints[JavaScriptSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher(envID, config, httpConfig, storeAdapter, loggers,
			"/events/bulk/"+string(envID))
		ep.endpoints[JavaScriptSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers,
			"/events/diagnostic/"+string(envID))
	}
	return ep
}

func newDiagnosticEventEndpointDispatcher(
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	loggers ldlog.Loggers,
	remotePath string,
) *diagnosticEventEndpointDispatcher {
	eventsURI := config.EventsURI.String()
	if eventsURI == "" {
		eventsURI = c.DefaultEventsURI
	}
	return &diagnosticEventEndpointDispatcher{
		httpClient:        httpConfig.Client(),
		httpConfig:        httpConfig,
		remoteEndpointURI: strings.TrimRight(eventsURI, "/") + remotePath,
		loggers:           loggers,
	}
}

func newAnalyticsEventEndpointDispatcher(
	authKey c.SDKCredential,
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	storeAdapter *store.SSERelayDataStoreAdapter,
	loggers ldlog.Loggers,
	remotePath string,
) *analyticsEventEndpointDispatcher {
	return &analyticsEventEndpointDispatcher{
		authKey:      authKey,
		config:       config,
		httpClient:   httpConfig.Client(),
		httpConfig:   httpConfig,
		storeAdapter: storeAdapter,
		loggers:      loggers,
		remotePath:   remotePath,
	}
}

func newEventVerbatimRelay(
	authKey c.SDKCredential,
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	loggers ldlog.Loggers,
	remotePath string,
) *eventVerbatimRelay {
	eventsURI := config.EventsURI.String()
	if eventsURI == "" {
		eventsURI = c.DefaultEventsURI
	}
	opts := []OptionType{
		OptionCapacity(config.Capacity.GetOrElse(c.DefaultEventCapacity)),
		OptionEndpointURI(strings.TrimRight(eventsURI, "/") + remotePath),
	}

	opts = append(opts, OptionFlushInterval(config.FlushInterval.GetOrElse(c.DefaultEventsFlushInterval)))

	publisher, _ := NewHTTPEventPublisher(authKey, httpConfig, loggers, opts...)

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
	er.publisher.PublishRaw(evts...)
}
