package middleware

import (
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
	"github.com/stretchr/testify/require"
)

const (
	metricsTestUserAgent = "fake-user-agent"
)

type metricsMiddlewareTestParams struct {
	env      relayenv.EnvContext
	envName  string
	exporter *st.TestMetricsExporter
	mockLog  *ldlogtest.MockLog
}

func metricsMiddlewareTest(t *testing.T, action func(metricsMiddlewareTestParams)) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	manager, err := metrics.NewManager(config.MetricsConfig{}, time.Millisecond*10, mockLog.Loggers)
	require.NoError(t, err)
	defer manager.Close()

	exporter := st.NewTestMetricsExporter()

	// Since the global OpenCensus state will accumulate metrics from different tests, we'll use a randomized
	// environment name to isolate the data from this particular test.
	envName := "testenv" // "env-" + uuid.New()

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

	exporter.WithExporter(func() {
		action(metricsMiddlewareTestParams{
			env:      env,
			envName:  envName,
			exporter: exporter,
			mockLog:  mockLog,
		})
	})
}

func TestCountConnections(t *testing.T) {
	t.Run("browser", func(t *testing.T) {
		testCountConnections(t, CountBrowserConns, "browser")
	})
	t.Run("mobile", func(t *testing.T) {
		testCountConnections(t, CountMobileConns, "mobile")
	})
	t.Run("browser", func(t *testing.T) {
		testCountConnections(t, CountServerConns, "server")
	})
}

func testCountConnections(t *testing.T, countFn func(http.Handler) http.Handler, category string) {
	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		expectedTags := map[string]string{
			"env":              p.envName,
			"platformCategory": category,
			"userAgent":        metricsTestUserAgent,
		}

		req, _ := http.NewRequest("GET", "", nil)
		req.Header.Set("User-Agent", metricsTestUserAgent)
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		rr := httptest.NewRecorder()

		countFn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.exporter.AwaitData(t, time.Second, p.mockLog.Loggers, func(d st.TestMetricsData) bool {
				return d.HasRow("connections", st.TestMetricsRow{
					Tags: expectedTags,
					Sum:  1,
				}) && d.HasRow("newconnections", st.TestMetricsRow{
					Tags: expectedTags,
					Sum:  1,
				})
			})
		})).ServeHTTP(rr, req)

		p.exporter.AwaitData(t, time.Second, p.mockLog.Loggers, func(d st.TestMetricsData) bool {
			return d.HasRow("connections", st.TestMetricsRow{
				Tags: expectedTags,
				Sum:  0,
			}) && d.HasRow("newconnections", st.TestMetricsRow{
				Tags: expectedTags,
				Sum:  1,
			})
		})
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
	// We need to build a router here because RequestCount expects mux.CurrentRoute() to work.
	router := mux.NewRouter()
	router.Use(RequestCount(measure))
	router.Handle("/test-route", nullHandler()).Methods("GET")

	metricsMiddlewareTest(t, func(p metricsMiddlewareTestParams) {
		expectedTags := map[string]string{
			"env":              p.envName,
			"method":           "GET",
			"route":            "_test-route",
			"platformCategory": category,
			"userAgent":        metricsTestUserAgent,
		}

		makeRequest := func() *http.Request {
			req, _ := http.NewRequest("GET", "/test-route", nil)
			req.Header.Set("User-Agent", metricsTestUserAgent)
			return req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: p.env}))
		}

		router.ServeHTTP(httptest.NewRecorder(), makeRequest())

		p.exporter.AwaitData(t, time.Second, p.mockLog.Loggers, func(d st.TestMetricsData) bool {
			return d.HasRow("requests", st.TestMetricsRow{
				Tags:  expectedTags,
				Count: 1,
			})
		})

		router.ServeHTTP(httptest.NewRecorder(), makeRequest())

		p.exporter.AwaitData(t, time.Second, p.mockLog.Loggers, func(d st.TestMetricsData) bool {
			return d.HasRow("requests", st.TestMetricsRow{
				Tags:  expectedTags,
				Count: 2,
			})
		})
	})
}
