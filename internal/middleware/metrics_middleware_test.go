package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

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
	mockLog *ldlogtest.MockLog
}

func (p metricsMiddlewareTestParams) collectMetrics(t *testing.T) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	err := p.reader.Collect(context.Background(), &rm)
	require.NoError(t, err)
	return &rm
}

func metricsMiddlewareTest(t *testing.T, action func(metricsMiddlewareTestParams)) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	manager, err := metrics.NewManager(config.OpenTelemetryConfig{}, time.Millisecond*10, mockLog.Loggers)
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
		Loggers:        mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	action(metricsMiddlewareTestParams{
		env:     env,
		envName: envName,
		reader:  reader,
		mockLog: mockLog,
	})
}

func TestCountConnections(t *testing.T) {
	t.Run("browser", func(t *testing.T) {
		testCountConnections(t, CountBrowserConns, "browser")
	})
	t.Run("mobile", func(t *testing.T) {
		testCountConnections(t, CountMobileConns, "mobile")
	})
	t.Run("server", func(t *testing.T) {
		testCountConnections(t, CountServerConns, "server")
	})
}

func testCountConnections(t *testing.T, countFn func(http.Handler) http.Handler, category string) {
	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		req, _ := http.NewRequest("GET", "", nil)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		rr := httptest.NewRecorder()

		countFn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// While inside the handler, connections should be active
			rm := p.collectMetrics(t)
			connMetric := st.FindMetricByName(rm, "launchdarkly.relay.connections")
			require.NotNil(t, connMetric, "connections metric not found")
			assertMetricHasValue(t, connMetric, p.envName, category, 1)
		})).ServeHTTP(rr, req)

		// After handler returns, connection gauge should be 0
		rm := p.collectMetrics(t)
		connMetric := st.FindMetricByName(rm, "launchdarkly.relay.connections")
		require.NotNil(t, connMetric, "connections metric not found")
		assertMetricHasValue(t, connMetric, p.envName, category, 0)
	})
}

func TestCountRequests(t *testing.T) {
	t.Run("browser", func(t *testing.T) {
		testCountRequests(t, metrics.BrowserRequests, "browser")
	})
	t.Run("mobile", func(t *testing.T) {
		testCountRequests(t, metrics.MobileRequests, "mobile")
	})
	t.Run("server", func(t *testing.T) {
		testCountRequests(t, metrics.ServerRequests, "server")
	})
}

func testCountRequests(t *testing.T, measure metrics.Measure, category string) {
	// We need to build a router here because RequestMetrics expects mux.CurrentRoute() to work.
	router := mux.NewRouter()
	router.Use(RequestMetrics(measure))
	router.Handle("/test-route", nullHandler()).Methods("GET")

	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		makeRequest := func() *http.Request {
			req, _ := http.NewRequest("GET", "/test-route", nil)
			req.Header.Set("User-Agent", metricsTestUserAgent)
			return req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		}

		router.ServeHTTP(httptest.NewRecorder(), makeRequest())

		rm := p.collectMetrics(t)
		reqMetric := st.FindMetricByName(rm, "launchdarkly.relay.requests")
		require.NotNil(t, reqMetric, "requests metric not found")
		assertMetricHasValue(t, reqMetric, p.envName, category, 1)

		router.ServeHTTP(httptest.NewRecorder(), makeRequest())

		rm = p.collectMetrics(t)
		reqMetric = st.FindMetricByName(rm, "launchdarkly.relay.requests")
		require.NotNil(t, reqMetric, "requests metric not found")
		assertMetricHasValue(t, reqMetric, p.envName, category, 2)
	})
}

func TestRequestDuration(t *testing.T) {
	router := mux.NewRouter()
	router.Use(RequestMetrics(metrics.ServerRequests))
	router.Handle("/test-route", nullHandler()).Methods("GET")

	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		req, _ := http.NewRequest("GET", "/test-route", nil)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		router.ServeHTTP(httptest.NewRecorder(), req)

		rm := p.collectMetrics(t)
		durMetric := st.FindMetricByName(rm, "launchdarkly.relay.request.duration")
		require.NotNil(t, durMetric, "request duration metric not found")
		hist, ok := durMetric.Data.(metricdata.Histogram[float64])
		require.True(t, ok, "expected Histogram[float64] data")
		require.NotEmpty(t, hist.DataPoints)
		assert.Equal(t, uint64(1), hist.DataPoints[0].Count, "expected 1 duration recording")
	})
}

// assertMetricHasValue checks that a metric has the expected value for the given environment and platform.
func assertMetricHasValue(t *testing.T, m *metricdata.Metrics, envName, platform string, expected int64) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	found := false
	for _, dp := range sum.DataPoints {
		platVal, platOK := dp.Attributes.Value(attribute.Key("platformCategory"))
		envVal, envOK := dp.Attributes.Value(attribute.Key("env"))
		if platOK && envOK && platVal.AsString() == platform && envVal.AsString() == envName {
			assert.Equal(t, expected, dp.Value, "unexpected value for %s (platform=%s, env=%s)", m.Name, platform, envName)
			found = true
		}
	}
	assert.True(t, found, "no data point found for %s with platform=%s, env=%s", m.Name, platform, envName)
}
