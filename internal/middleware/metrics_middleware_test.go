package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	metricsTestUserAgent = "fake-user-agent"
)

type metricsMiddlewareTestParams struct {
	env     relayenv.EnvContext
	envName string
	reader  sdkmetric.Reader
	manager *metrics.Manager
}

func (p metricsMiddlewareTestParams) collectMetrics(t *testing.T) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	err := p.reader.Collect(context.Background(), &rm)
	require.NoError(t, err)
	return &rm
}

func metricsMiddlewareTest(t *testing.T, action func(metricsMiddlewareTestParams)) {
	manager, err := metrics.NewManager(config.OpenTelemetryConfig{}, time.Millisecond*10, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	// Set up OTel test infrastructure
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	instruments, err := metrics.NewInstrumentsForTest(meterProvider.Meter("ld-relay"))
	require.NoError(t, err)
	manager.SetInstrumentsForTest(instruments)

	envName := "testenv"
	envConfig := config.EnvConfig{}
	allConfig := config.Config{}

	env, err := relayenv.NewEnvContext(relayenv.EnvContextImplParams{
		Identifiers:    relayenv.EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:      envConfig,
		AllConfig:      allConfig,
		ClientFactory:  testclient.FakeLDClientFactory(true),
		MetricsManager: manager,
		LogNameMode:    relayenv.LogNameIsEnvID,
		Logger:         slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	action(metricsMiddlewareTestParams{
		env:     env,
		envName: envName,
		reader:  reader,
		manager: manager,
	})
}

// The CountXConns middlewares feed only the stream-connection counts that Relay reports back to
// LaunchDarkly. http.server.active_requests is handled by RequestMetrics, which wraps every endpoint,
// so counting here as well would double count every streaming request.
func TestCountConnectionsDoesNotRecordActiveRequests(t *testing.T) {
	specs := []struct {
		name    string
		countFn func(http.Handler) http.Handler
	}{
		{name: "browser", countFn: CountBrowserConns},
		{name: "mobile", countFn: CountMobileConns},
		{name: "server", countFn: CountServerConns},
	}

	for _, tt := range specs {
		t.Run(tt.name, func(t *testing.T) {
			metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
				req, _ := http.NewRequest("GET", "", nil)
				req.Header.Set("User-Agent", metricsTestUserAgent)
				req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))

				tt.countFn(nullHandler()).ServeHTTP(httptest.NewRecorder(), req)

				rm := p.collectMetrics(t)
				assert.Nil(t, st.FindMetricByName(rm, "http.server.active_requests"),
					"stream connection counting must not touch the active requests instrument")
			})
		})
	}
}

// Active requests must be tracked for every kind of endpoint, not just streams, per the OTEL semantic
// convention for http.server.active_requests.
func TestActiveRequests(t *testing.T) {
	endpointTypes := []metrics.EndpointType{
		metrics.EndpointTypeStream,
		metrics.EndpointTypePoll,
		metrics.EndpointTypeEvents,
		metrics.EndpointTypeGoals,
	}

	for _, endpointType := range endpointTypes {
		t.Run(string(endpointType), func(t *testing.T) {
			metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
				router := mux.NewRouter()
				router.Use(RequestMetrics(endpointType))
				router.Handle("/test-route", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// While inside the handler, the request should be counted as active
					rm := p.collectMetrics(t)
					m := st.FindMetricByName(rm, "http.server.active_requests")
					require.NotNil(t, m, "active requests metric not found")
					assertMetricHasValue(t, m, p.envName, 1)
					assertMetricHasAttribute(t, m, "relay.endpoint.type", string(endpointType))
				})).Methods("GET")

				req, _ := http.NewRequest("GET", "/test-route", nil)
				req.Header.Set("User-Agent", metricsTestUserAgent)
				req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
				router.ServeHTTP(httptest.NewRecorder(), req)

				// Once the request finishes it should return to 0, on the same series
				rm := p.collectMetrics(t)
				m := st.FindMetricByName(rm, "http.server.active_requests")
				require.NotNil(t, m, "active requests metric not found")
				assertMetricHasValue(t, m, p.envName, 0)
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				assert.Len(t, sum.DataPoints, 1, "increment and decrement should share one attribute set")
			})
		})
	}
}

// A streaming response is counted as an active request for as long as it is held open, even though its
// duration is deliberately not recorded.
func TestActiveRequestsIncludesStreamingResponses(t *testing.T) {
	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		router := mux.NewRouter()
		router.Use(RequestMetrics(metrics.EndpointTypeStream))
		router.Handle("/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			rm := p.collectMetrics(t)
			m := st.FindMetricByName(rm, "http.server.active_requests")
			require.NotNil(t, m, "active requests metric not found")
			assertMetricHasValue(t, m, p.envName, 1)
		})).Methods("GET")

		req, _ := http.NewRequest("GET", "/stream", nil)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := p.collectMetrics(t)
		m := st.FindMetricByName(rm, "http.server.active_requests")
		require.NotNil(t, m)
		assertMetricHasValue(t, m, p.envName, 0)

		assert.Nil(t, st.FindMetricByName(rm, "http.server.request.duration"),
			"duration must not be recorded for a streaming response")
	})
}

// Requests with no environment -- the status endpoints and anything that matched no route -- still get
// counted, reporting the not_provided sentinel for environment.name.
func TestUnscopedActiveRequests(t *testing.T) {
	specs := []struct {
		name         string
		endpointType metrics.EndpointType
	}{
		{name: "status", endpointType: metrics.EndpointTypeStatus},
		{name: "unmatched", endpointType: metrics.EndpointTypeNotProvided},
	}

	for _, tt := range specs {
		t.Run(tt.name, func(t *testing.T) {
			metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
				handler := UnscopedActiveRequests(p.manager, tt.endpointType)(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						rm := p.collectMetrics(t)
						m := st.FindMetricByName(rm, "http.server.active_requests")
						require.NotNil(t, m, "active requests metric not found")
						assertMetricHasValue(t, m, "not_provided", 1)
						assertMetricHasAttribute(t, m, "relay.endpoint.type", string(tt.endpointType))
					}))

				// No environment context is attached, exactly as for a real status or unmatched request
				req, _ := http.NewRequest("GET", "/status", nil)
				handler.ServeHTTP(httptest.NewRecorder(), req)

				rm := p.collectMetrics(t)
				m := st.FindMetricByName(rm, "http.server.active_requests")
				require.NotNil(t, m)
				assertMetricHasValue(t, m, "not_provided", 0)
			})
		})
	}
}

func TestRequestDuration(t *testing.T) {
	router := mux.NewRouter()
	router.Use(RequestMetrics(metrics.EndpointTypePoll))
	router.Handle("/test-route", nullHandler()).Methods("GET")

	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		req, _ := http.NewRequest("GET", "/test-route", nil)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := p.collectMetrics(t)
		durMetric := st.FindMetricByName(rm, "http.server.request.duration")
		require.NotNil(t, durMetric, "request duration metric not found")
		hist, ok := durMetric.Data.(metricdata.Histogram[float64])
		require.True(t, ok, "expected Histogram[float64] data")
		require.NotEmpty(t, hist.DataPoints)
		assert.Equal(t, uint64(1), hist.DataPoints[0].Count, "expected 1 duration recording")
	})
}

func TestEventBytesMetrics(t *testing.T) {
	router := mux.NewRouter()
	router.Use(EventBytesMetrics())
	router.Handle("/events", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Consume the body so counting reader sees the bytes
		_, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusAccepted)
	})).Methods("POST")

	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		body := strings.NewReader(`[{"kind":"identify","key":"user1"}]`)
		req, _ := http.NewRequest("POST", "/events", body)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := p.collectMetrics(t)
		bytesMetric := st.FindMetricByName(rm, "launchdarkly.relay.events.received.size")
		require.NotNil(t, bytesMetric, "events received bytes metric not found")
		assertMetricHasValue(t, bytesMetric, p.envName, 35)
	})
}

func TestParseApplicationTags(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		wantID  string
		wantVer string
	}{
		{"both present", "application-id/my-app application-version/1.0.0", "my-app", "1.0.0"},
		{"only id", "application-id/my-app", "my-app", ""},
		{"only version", "application-version/2.0.0", "", "2.0.0"},
		{"empty header", "", "", ""},
		{"unknown keys ignored", "foo/bar application-id/my-app baz/qux", "my-app", ""},
		{"extra spaces", "application-id/my-app  application-version/1.0.0", "my-app", "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("X-LaunchDarkly-Tags", tt.header)
			}
			gotID, gotVer := parseApplicationTags(req)
			assert.Equal(t, tt.wantID, gotID)
			assert.Equal(t, tt.wantVer, gotVer)
		})
	}
}

// assertMetricHasValue checks that a metric has the expected value for the given environment and platform.
func assertMetricHasValue(t *testing.T, m *metricdata.Metrics, envName string, expected int64) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	found := false
	for _, dp := range sum.DataPoints {
		envVal, envOK := dp.Attributes.Value(attribute.Key("environment.name"))
		if envOK && envVal.AsString() == envName {
			assert.Equal(t, expected, dp.Value, "unexpected value for %s (env=%s)", m.Name, envName)
			found = true
		}
	}
	assert.True(t, found, "no data point found for %s with env=%s", m.Name, envName)
}

// assertMetricHasAttribute checks that every data point of a metric carries the expected attribute value.
func assertMetricHasAttribute(t *testing.T, m *metricdata.Metrics, key, expected string) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	require.NotEmpty(t, sum.DataPoints)
	for _, dp := range sum.DataPoints {
		val, found := dp.Attributes.Value(attribute.Key(key))
		require.True(t, found, "%s attribute not present on %s", key, m.Name)
		assert.Equal(t, expected, val.AsString())
	}
}
