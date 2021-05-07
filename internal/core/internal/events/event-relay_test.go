package events

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	eventPayloadForVerbatimOnly = `["fake-event-1","fake-event-2","fake-event-3"]`
	// when events are proxied verbatim, we don't care about the schema, it just has to be valid JSON
)

type testEndpointInfo struct {
	sdkKind        basictypes.SDKKind
	analyticsPath  string
	diagnosticPath string
	authKey        string
	newCredential  config.SDKCredential
}

var testServerEndpointInfo = testEndpointInfo{
	sdkKind:        basictypes.ServerSDK,
	analyticsPath:  "/bulk",
	diagnosticPath: "/diagnostic",
	authKey:        string(st.EnvWithAllCredentials.Config.SDKKey),
	newCredential:  config.SDKKey(string(st.EnvWithAllCredentials.Config.SDKKey) + "-new"),
}

var testMobileEndpointInfo = testEndpointInfo{
	sdkKind:        basictypes.MobileSDK,
	analyticsPath:  "/mobile",
	diagnosticPath: "/mobile/events/diagnostic",
	authKey:        string(st.EnvWithAllCredentials.Config.MobileKey),
	newCredential:  config.MobileKey(string(st.EnvWithAllCredentials.Config.MobileKey) + "-new"),
}

var testJSClientEndpointInfo = testEndpointInfo{
	sdkKind:        basictypes.JSClientSDK,
	analyticsPath:  "/events/bulk/" + string(st.EnvWithAllCredentials.Config.EnvID),
	diagnosticPath: "/events/diagnostic/" + string(st.EnvWithAllCredentials.Config.EnvID),
}
var allTestEndpoints = []testEndpointInfo{testServerEndpointInfo, testMobileEndpointInfo, testJSClientEndpointInfo}

type eventRelayTestParams struct {
	dispatcher *EventDispatcher
	requestsCh <-chan httphelpers.HTTPRequestInfo
	dataStore  interfaces.DataStore
	mockLog    *ldlogtest.MockLog
}

func eventRelayTest(
	t *testing.T,
	testEnv st.TestEnv,
	eventsConfig config.EventsConfig,
	fn func(eventRelayTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	httpConfig, _ := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", mockLog.Loggers)

	store := st.NewInMemoryStore()

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		eventsConfig.SendEvents = true
		if !eventsConfig.FlushInterval.IsDefined() {
			// by default, set a long flush interval so tests can control when a flush will happen
			eventsConfig.FlushInterval = configtypes.NewOptDuration(time.Hour)
		}
		eventsConfig.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

		dispatcher := NewEventDispatcher(
			testEnv.Config.SDKKey,
			testEnv.Config.MobileKey,
			testEnv.Config.EnvID,
			mockLog.Loggers,
			eventsConfig,
			httpConfig,
			makeStoreAdapterWithExistingStore(store),
		)
		defer dispatcher.Close()

		p := eventRelayTestParams{
			dispatcher: dispatcher,
			requestsCh: requestsCh,
			dataStore:  store,
			mockLog:    mockLog,
		}
		fn(p)
	})
}

func TestEventDispatcherCreatesHandlersOnlyForConfiguredCredentials(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.DiagnosticEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.DiagnosticEventDataKind))
	})
	eventRelayTest(t, st.EnvMobile, config.EventsConfig{}, func(p eventRelayTestParams) {
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.DiagnosticEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind))
		assert.Nil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.DiagnosticEventDataKind))
	})
	eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.MobileSDK, ldevents.DiagnosticEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind))
		assert.NotNil(t, p.dispatcher.GetHandler(basictypes.JSClientSDK, ldevents.DiagnosticEventDataKind))
	})
}

func TestVerbatimEventHandlers(t *testing.T) {
	for _, e := range allTestEndpoints {
		t.Run(string(e.sdkKind), func(t *testing.T) {
			eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
				req := st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly),
					headersWithEventSchema(SummaryEventsSchemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()

				r := st.ExpectTestRequest(t, p.requestsCh, time.Second)
				assert.Equal(t, "POST", r.Request.Method)
				assert.Equal(t, e.analyticsPath, r.Request.URL.Path)
				assert.Equal(t, e.authKey, r.Request.Header.Get("Authorization"))
				assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
				assert.Equal(t, eventPayloadForVerbatimOnly, string(r.Body))
			})
		})
	}
}

func TestSummarizingEventHandlers(t *testing.T) {
	// The summarizing relay logic is tested in more detail in summarizing-relay_test.go. The test here
	// just verifies that we are indeed using the summarizing relay for these endpoints.
	for _, e := range allTestEndpoints {
		t.Run(string(e.sdkKind), func(t *testing.T) {
			eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
				req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()

				r := st.ExpectTestRequest(t, p.requestsCh, time.Second)
				assert.Equal(t, "POST", r.Request.Method)
				assert.Equal(t, e.analyticsPath, r.Request.URL.Path)
				assert.Equal(t, e.authKey, r.Request.Header.Get("Authorization"))
				assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
				assert.JSONEq(t, expectedSummarizedFeatureEventsOutputUnknownFlagWithVersion, string(r.Body))
			})
		})
	}
}

func TestDiagnosticEventForwarding(t *testing.T) {
	for _, e := range allTestEndpoints {
		t.Run(string(e.sdkKind), func(t *testing.T) {
			eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
				req := st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly), headersWithEventSchema(0))
				req.Header.Add("Authorization", "fake-auth")
				req.Header.Add("User-Agent", "fake-user-agent")
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.DiagnosticEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				r := st.ExpectTestRequest(t, p.requestsCh, time.Second)
				assert.Equal(t, "POST", r.Request.Method)
				assert.Equal(t, e.diagnosticPath, r.Request.URL.Path)
				assert.Equal(t, "fake-auth", r.Request.Header.Get("Authorization"))
				assert.Equal(t, "fake-user-agent", r.Request.Header.Get("User-Agent"))
				assert.Equal(t, eventPayloadForVerbatimOnly, string(r.Body))
			})
		})
	}
}

func TestEventDispatcherReplaceCredential(t *testing.T) {
	eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
		// First, just post some events to all the dispatchers to make sure they've been lazily created.
		// We don't need to check for the original credential in the request headers, because we already
		// have other tests verifying that behavior.
		for _, e := range allTestEndpoints {
			if e.newCredential == nil {
				continue
			}
			for _, schemaVersion := range []int{0, 2} {
				req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(schemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()
				_ = st.ExpectTestRequest(t, p.requestsCh, time.Second)
			}
		}

		// Now change both the SDK key and the mobile key (the environment ID can't change)
		p.dispatcher.ReplaceCredential(testServerEndpointInfo.newCredential)
		p.dispatcher.ReplaceCredential(testMobileEndpointInfo.newCredential)

		// Verify that proxied events now use the new credentials
		for _, e := range allTestEndpoints {
			if e.newCredential == nil {
				continue
			}
			for _, schemaVersion := range []int{0, 2} {
				req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(schemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()
				r := st.ExpectTestRequest(t, p.requestsCh, time.Second)
				assert.Equal(t, e.newCredential.GetAuthorizationHeaderValue(), r.Request.Header.Get("Authorization"))
			}
		}
	})
}

func TestEventHandlersRejectMalformedJSON(t *testing.T) {
	malformedInput := `[{"no`
	eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
		for _, e := range allTestEndpoints {
			for _, schemaVersion := range []int{0, 2} {
				req := st.BuildRequest("POST", "/", []byte(malformedInput), headersWithEventSchema(schemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()
				st.ExpectNoTestRequests(t, p.requestsCh, time.Millisecond*20)
			}
		}
	})
}

func TestEventHandlersRejectEmptyBody(t *testing.T) {
	eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
		for _, e := range allTestEndpoints {
			for _, schemaVersion := range []int{0, 2} {
				req := st.BuildRequest("POST", "/", []byte{}, headersWithEventSchema(schemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
			}
		}
	})
}

func headersWithEventSchema(schemaVersion int) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if schemaVersion > 0 {
		headers.Set(EventSchemaHeader, strconv.Itoa(schemaVersion))
	}
	return headers
}
