package events

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/store"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ldevents "github.com/launchdarkly/go-sdk-events/v2"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	m "github.com/launchdarkly/go-test-helpers/v3/matchers"

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
	newCredential  credential.SDKCredential
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

type eventRelayTestOptions struct {
	eventQueueCleanupInterval time.Duration
}

type eventRelayTestParams struct {
	dispatcher *EventDispatcher
	requestsCh <-chan httphelpers.HTTPRequestInfo
	dataStore  subsystems.DataStore
	mockLog    *ldlogtest.MockLog
}

func eventRelayTest(
	t *testing.T,
	testEnv st.TestEnv,
	eventsConfig config.EventsConfig,
	fn func(eventRelayTestParams),
) {
	eventRelayTestWithOptions(t, testEnv, eventsConfig, eventRelayTestOptions{}, fn)
}

func eventRelayTestWithOptions(
	t *testing.T,
	testEnv st.TestEnv,
	eventsConfig config.EventsConfig,
	opts eventRelayTestOptions,
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
			opts.eventQueueCleanupInterval,
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
	t.Run("single schema version", func(t *testing.T) {
		for _, e := range allTestEndpoints {
			t.Run(string(e.sdkKind), func(t *testing.T) {
				eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
					req := st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly),
						headersWithEventSchema(CurrentEventsSchemaVersion))
					handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
					require.NotNil(t, handler)
					w := httptest.NewRecorder()
					handler(w, req)
					assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

					p.dispatcher.flush()

					r := helpers.RequireValue(t, p.requestsCh, time.Second)
					assert.Equal(t, "POST", r.Request.Method)
					assert.Equal(t, e.analyticsPath, r.Request.URL.Path)
					assert.Equal(t, e.authKey, r.Request.Header.Get("Authorization"))
					assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
					assert.Equal(t, eventPayloadForVerbatimOnly, string(r.Body))
				})
			})
		}
	})

	t.Run("multiple schema versions", func(t *testing.T) {
		for _, e := range allTestEndpoints {
			t.Run(string(e.sdkKind), func(t *testing.T) {
				eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
					req1 := st.BuildRequest("POST", "/", []byte(`["fake-event-v3-1","fake-event-v3-2"]`),
						headersWithEventSchema(3))
					req2 := st.BuildRequest("POST", "/", []byte(`["fake-event-v4-1","fake-event-v4-2"]`),
						headersWithEventSchema(4))
					req3 := st.BuildRequest("POST", "/", []byte(`["fake-event-v3-3"]`),
						headersWithEventSchema(3))
					for _, req := range []*http.Request{req1, req2, req3} {
						handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
						require.NotNil(t, handler)
						w := httptest.NewRecorder()
						handler(w, req)
						assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)
					}

					p.dispatcher.flush()

					received := []httphelpers.HTTPRequestInfo{
						helpers.RequireValue(t, p.requestsCh, time.Second),
						helpers.RequireValue(t, p.requestsCh, time.Second),
					}
					sort.Slice(received, func(i, j int) bool { return string(received[i].Body) < string(received[j].Body) })
					for _, r := range received {
						assert.Equal(t, "POST", r.Request.Method)
						assert.Equal(t, e.analyticsPath, r.Request.URL.Path)
						assert.Equal(t, e.authKey, r.Request.Header.Get("Authorization"))
					}
					assert.Equal(t, "3", received[0].Request.Header.Get(EventSchemaHeader))
					assert.Equal(t, `["fake-event-v3-1","fake-event-v3-2","fake-event-v3-3"]`, string(received[0].Body))
					assert.Equal(t, "4", received[1].Request.Header.Get(EventSchemaHeader))
					assert.Equal(t, `["fake-event-v4-1","fake-event-v4-2"]`, string(received[1].Body))
				})
			})
		}
	})
}

func TestSummarizingEventHandlers(t *testing.T) {
	// The summarizing relay logic is tested in more detail in summarizing-relay_test.go. The test here
	// just verifies that we are indeed using the summarizing relay for these endpoints.
	summarizeEventsParams := makeBasicSummarizeEventsParams()
	for _, e := range allTestEndpoints {
		t.Run(string(e.sdkKind), func(t *testing.T) {
			eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
				req := st.BuildRequest("POST", "/", []byte(summarizeEventsParams.inputEventsJSON),
					headersWithEventSchema(summarizeEventsParams.schemaVersion))
				handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
				require.NotNil(t, handler)
				w := httptest.NewRecorder()
				handler(w, req)
				assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

				p.dispatcher.flush()

				r := helpers.RequireValue(t, p.requestsCh, time.Second)
				assert.Equal(t, "POST", r.Request.Method)
				assert.Equal(t, e.analyticsPath, r.Request.URL.Path)
				assert.Equal(t, e.authKey, r.Request.Header.Get("Authorization"))
				assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
				m.In(t).Assert(r.Body, m.JSONStrEqual(summarizeEventsParams.expectedEventsJSON))
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

				r := helpers.RequireValue(t, p.requestsCh, time.Second)
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
	summarizeEventsParams := makeBasicSummarizeEventsParams()

	eventRelayTest(t, st.EnvWithAllCredentials, config.EventsConfig{}, func(p eventRelayTestParams) {
		// First, just post some events to all the dispatchers to make sure they've been lazily created.
		// We don't need to check for the original credential in the request headers, because we already
		// have other tests verifying that behavior.
		for _, e := range allTestEndpoints {
			if e.newCredential == nil {
				continue
			}
			req := st.BuildRequest("POST", "/", []byte(summarizeEventsParams.inputEventsJSON),
				headersWithEventSchema(summarizeEventsParams.schemaVersion))
			handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
			require.NotNil(t, handler)
			w := httptest.NewRecorder()
			handler(w, req)
			assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

			p.dispatcher.flush()
			_ = helpers.RequireValue(t, p.requestsCh, time.Second)
		}

		// Now change both the SDK key and the mobile key (the environment ID can't change)
		p.dispatcher.ReplaceCredential(testServerEndpointInfo.newCredential)
		p.dispatcher.ReplaceCredential(testMobileEndpointInfo.newCredential)

		// Verify that proxied events now use the new credentials
		for _, e := range allTestEndpoints {
			if e.newCredential == nil {
				continue
			}
			req := st.BuildRequest("POST", "/", []byte(summarizeEventsParams.inputEventsJSON),
				headersWithEventSchema(summarizeEventsParams.schemaVersion))
			handler := p.dispatcher.GetHandler(e.sdkKind, ldevents.AnalyticsEventDataKind)
			require.NotNil(t, handler)
			w := httptest.NewRecorder()
			handler(w, req)
			assert.Equal(t, http.StatusAccepted, w.Result().StatusCode)

			p.dispatcher.flush()
			r := helpers.RequireValue(t, p.requestsCh, time.Second)
			assert.Equal(t, e.newCredential.GetAuthorizationHeaderValue(), r.Request.Header.Get("Authorization"))
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
				if !helpers.AssertNoMoreValues(t, p.requestsCh, time.Millisecond*20) {
					t.FailNow()
				}
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

func makeStoreAdapterWithExistingStore(s subsystems.DataStore) *store.SSERelayDataStoreAdapter {
	a := store.NewSSERelayDataStoreAdapter(st.ExistingInstance(s), nil)
	_, _ = a.Build(subsystems.BasicClientContext{}) // ensure the wrapped store has been created
	return a
}
