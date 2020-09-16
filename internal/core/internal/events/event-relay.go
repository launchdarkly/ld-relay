package events

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/util"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"

	"github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
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
	analyticsEndpoints  map[sdks.Kind]*analyticsEventEndpointDispatcher
	diagnosticEndpoints map[sdks.Kind]*diagnosticEventEndpointDispatcher
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

// GetHandler returns the HTTP handler for an endpoint, or nil if none is defined
func (r *EventDispatcher) GetHandler(sdkKind sdks.Kind, eventsKind ldevents.EventDataKind) func(w http.ResponseWriter, req *http.Request) {
	if eventsKind == ldevents.DiagnosticEventDataKind {
		if e, ok := r.diagnosticEndpoints[sdkKind]; ok {
			return e.dispatch
		}
	} else {
		if e, ok := r.analyticsEndpoints[sdkKind]; ok {
			return e.dispatch
		}
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

func (r *analyticsEventEndpointDispatcher) replaceCredential(newCredential c.SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reflect.TypeOf(r.authKey) == reflect.TypeOf(newCredential) {
		r.authKey = newCredential
		if r.summarizingRelay != nil {
			r.summarizingRelay.eventSender.replaceCredential(newCredential)
		}
		if r.verbatimRelay != nil {
			r.verbatimRelay.publisher.ReplaceCredential(newCredential)
		}
	}
}

func (r *analyticsEventEndpointDispatcher) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summarizingRelay != nil {
		r.summarizingRelay.close()
	}
	if r.verbatimRelay != nil {
		r.verbatimRelay.close()
	}
}

func (d *diagnosticEventEndpointDispatcher) dispatch(w http.ResponseWriter, req *http.Request) {
	consumeEvents(w, req, d.loggers, func(body []byte) {
		// We are just operating as a reverse proxy and passing the request on verbatim to LD; we do not
		// need to parse the JSON.
		d.loggers.Debugf("Received diagnostic event to be proxied to %s", d.remoteEndpointURI)

		// We use the default EventSender from ldevents, which provides the standard retry logic and logging.
		// Since we don't want to use a fixed set of headers, but instead pass along the same headers we got
		// from the request, we're creating a new EventSender each time; that's a little inefficient, but
		// diagnostic events are relatively infrequent.
		sender := ldevents.NewDefaultEventSender(d.httpClient, "", d.remoteEndpointURI, req.Header, d.loggers)
		_ = sender.SendEventData(ldevents.DiagnosticEventDataKind, body, 1)
	})
}

func consumeEvents(w http.ResponseWriter, req *http.Request, loggers ldlog.Loggers, thenExecute func([]byte)) {
	body, bodyErr := ioutil.ReadAll(req.Body)

	if bodyErr != nil { // COVERAGE: can't make this happen in unit tests
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

	defer func() {
		if err := recover(); err != nil { // COVERAGE: can't make this happen in unit tests
			loggers.Errorf("Unexpected panic in event relay: %+v", err)
		}
	}()
	thenExecute(body)
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
		r.summarizingRelay = newEventSummarizingRelay(r.config, r.httpConfig, r.authKey, r.storeAdapter, r.loggers, r.remotePath)
	}
	return r.summarizingRelay
}

func (r *analyticsEventEndpointDispatcher) flush() { //nolint:unused // used only in tests
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbatimRelay != nil {
		r.verbatimRelay.publisher.Flush()
	}
	if r.summarizingRelay != nil {
		r.summarizingRelay.eventProcessor.Flush()
	}
}

// NewEventDispatcher creates a handler for relaying events to LaunchDarkly for an environment
func NewEventDispatcher(
	sdkKey c.SDKKey,
	mobileKey c.MobileKey,
	envID c.EnvironmentID,
	loggers ldlog.Loggers,
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	storeAdapter *store.SSERelayDataStoreAdapter,
) *EventDispatcher {
	ep := &EventDispatcher{
		analyticsEndpoints: map[sdks.Kind]*analyticsEventEndpointDispatcher{
			sdks.Server: newAnalyticsEventEndpointDispatcher(sdkKey,
				config, httpConfig, storeAdapter, loggers, "/bulk"),
		},
		diagnosticEndpoints: map[sdks.Kind]*diagnosticEventEndpointDispatcher{
			sdks.Server: newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers, "/diagnostic"),
		},
	}
	if mobileKey != "" {
		ep.analyticsEndpoints[sdks.Mobile] = newAnalyticsEventEndpointDispatcher(mobileKey,
			config, httpConfig, storeAdapter, loggers, "/mobile")
		ep.diagnosticEndpoints[sdks.Mobile] = newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers, "/mobile/events/diagnostic")
	}
	if envID != "" {
		ep.analyticsEndpoints[sdks.JSClient] = newAnalyticsEventEndpointDispatcher(envID, config, httpConfig, storeAdapter, loggers,
			"/events/bulk/"+string(envID))
		ep.diagnosticEndpoints[sdks.JSClient] = newDiagnosticEventEndpointDispatcher(config, httpConfig, loggers,
			"/events/diagnostic/"+string(envID))
	}
	return ep
}

// Close shuts down any goroutines/channels being used by the EventDispatcher.
func (r *EventDispatcher) Close() {
	for _, e := range r.analyticsEndpoints {
		e.close()
	}
	// diagnosticEventEndpointDispatcher doesn't currently need to be closed, because it doesn't maintain any
	// goroutines or channels
}

func (r *EventDispatcher) flush() { //nolint:unused // used only in tests
	for _, e := range r.analyticsEndpoints {
		e.flush()
	}
}

// ReplaceCredential changes the authorization credentail that is used when forwarding events to any
// endpoints that use that type of credential. For instance, if newCredential is a MobileKey, this
// affects only endpoints that use a mobile key.
func (r *EventDispatcher) ReplaceCredential(newCredential c.SDKCredential) {
	for _, d := range r.analyticsEndpoints {
		d.replaceCredential(newCredential)
	}
	// diagnosticEventEndpointDispatcher doesn't need to be updated for this, because it always uses whatever
	// credential was present on the incoming request
}

func newDiagnosticEventEndpointDispatcher(
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	loggers ldlog.Loggers,
	remotePath string,
) *diagnosticEventEndpointDispatcher {
	eventsURI := getEventsURI(config)
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
	eventsURI := getEventsURI(config)
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
	er.publisher.Publish(evts...)
}

func (er *eventVerbatimRelay) close() {
	er.publisher.Close()
}

func getEventsURI(config c.EventsConfig) string {
	return configtypes.NewOptStringNonEmpty(config.EventsURI.String()).GetOrElse(c.DefaultEventsURI)
}
