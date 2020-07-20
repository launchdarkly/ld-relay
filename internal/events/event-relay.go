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

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"github.com/launchdarkly/ld-relay/v6/internal/util"
)

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
		r.summarizingRelay = newEventSummarizingRelay(r.authKey, r.config, r.httpConfig, r.storeAdapter, r.loggers, r.remotePath)
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
	httpClient := httpConfig.Client()
	ep := &EventDispatcher{
		endpoints: map[Endpoint]eventEndpointDispatcher{
			ServerSDKEventsEndpoint: newAnalyticsEventEndpointDispatcher(sdkKey,
				config, httpConfig, httpClient, storeAdapter, loggers, "/bulk"),
		},
	}
	ep.endpoints[ServerSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers, "/diagnostic")
	if mobileKey != "" {
		ep.endpoints[MobileSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher(mobileKey,
			config, httpConfig, httpClient, storeAdapter, loggers, "/mobile")
		ep.endpoints[MobileSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers, "/mobile/events/diagnostic")
	}
	if envID != "" {
		ep.endpoints[JavaScriptSDKEventsEndpoint] = newAnalyticsEventEndpointDispatcher(envID, config, httpConfig, httpClient, storeAdapter, loggers,
			"/events/bulk/"+string(envID))
		ep.endpoints[JavaScriptSDKDiagnosticEventsEndpoint] = newDiagnosticEventEndpointDispatcher(config, httpClient, loggers,
			"/events/diagnostic/"+string(envID))
	}
	return ep
}

func newDiagnosticEventEndpointDispatcher(config c.EventsConfig, httpClient *http.Client, loggers ldlog.Loggers, remotePath string) *diagnosticEventEndpointDispatcher {
	return &diagnosticEventEndpointDispatcher{
		httpClient:        httpClient,
		remoteEndpointURI: strings.TrimRight(config.EventsURI.StringOrElse(c.DefaultEventsURI), "/") + remotePath,
		loggers:           loggers,
	}
}

func newAnalyticsEventEndpointDispatcher(authKey c.SDKCredential, config c.EventsConfig, httpConfig httpconfig.HTTPConfig,
	httpClient *http.Client, storeAdapter *store.SSERelayDataStoreAdapter, loggers ldlog.Loggers, remotePath string) *analyticsEventEndpointDispatcher {
	return &analyticsEventEndpointDispatcher{
		authKey:      authKey,
		config:       config,
		httpConfig:   httpConfig,
		httpClient:   httpClient,
		storeAdapter: storeAdapter,
		loggers:      loggers,
		remotePath:   remotePath,
	}
}

func newEventVerbatimRelay(authKey c.SDKCredential, config c.EventsConfig, httpClient *http.Client, loggers ldlog.Loggers, remotePath string) *eventVerbatimRelay {
	opts := []OptionType{
		OptionCapacity(config.Capacity),
		OptionEndpointURI(strings.TrimRight(config.EventsURI.StringOrElse(c.DefaultEventsURI), "/") + remotePath),
		OptionClient{Client: httpClient},
	}

	opts = append(opts, OptionFlushInterval(config.FlushInterval.GetOrElse(c.DefaultEventsFlushInterval)))

	publisher, _ := NewHttpEventPublisher(authKey, loggers, opts...)

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
