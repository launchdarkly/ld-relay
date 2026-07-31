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
			connMetric := st.FindMetricByName(rm, "http.server.active_requests")
			require.NotNil(t, connMetric, "connections metric not found")
			assertMetricHasValue(t, connMetric, p.envName, category, 1)
		})).ServeHTTP(rr, req)

		// After handler returns, connection gauge should be 0
		rm := p.collectMetrics(t)
		connMetric := st.FindMetricByName(rm, "http.server.active_requests")
		require.NotNil(t, connMetric, "connections metric not found")
		assertMetricHasValue(t, connMetric, p.envName, category, 0)
	})
}

func TestRequestDuration(t *testing.T) {
	router := mux.NewRouter()
	router.Use(DurationMetrics(metrics.ServerDuration))
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
	router.Use(EventBytesMetrics(metrics.ServerPlatformCategory))
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
		assertMetricHasValue(t, bytesMetric, p.envName, "server", 35)
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
func assertMetricHasValue(t *testing.T, m *metricdata.Metrics, envName, platform string, expected int64) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	found := false
	for _, dp := range sum.DataPoints {
		platVal, platOK := dp.Attributes.Value(attribute.Key("platform.category"))
		envVal, envOK := dp.Attributes.Value(attribute.Key("environment.name"))
		if platOK && envOK && platVal.AsString() == platform && envVal.AsString() == envName {
			assert.Equal(t, expected, dp.Value, "unexpected value for %s (platform=%s, env=%s)", m.Name, platform, envName)
			found = true
		}
	}
	assert.True(t, found, "no data point found for %s with platform=%s, env=%s", m.Name, platform, envName)
}

// deadlineProbeWriter is a ResponseWriter that records whether SetWriteDeadline reached it
// through a wrapper chain.
type deadlineProbeWriter struct {
	http.ResponseWriter
	gotWriteDeadline bool
}

func (d *deadlineProbeWriter) SetWriteDeadline(time.Time) error {
	d.gotWriteDeadline = true
	return nil
}

// TestStatusRecorderUnwrapExposesConnectionDeadline guards the Unwrap contract that the
// init-delivery limiter relies on: if statusRecorder does not implement Unwrap,
// http.NewResponseController cannot reach the underlying connection, and the limiter's
// read/write deadlines silently become no-ops (a slow client would then park a budget slot
// indefinitely).
func TestStatusRecorderUnwrapExposesConnectionDeadline(t *testing.T) {
	base := &deadlineProbeWriter{ResponseWriter: httptest.NewRecorder()}
	sr := &statusRecorder{ResponseWriter: base, statusCode: 200}
	err := http.NewResponseController(sr).SetWriteDeadline(time.Now().Add(time.Second))
	assert.NoError(t, err, "controller could not set a deadline through statusRecorder")
	assert.True(t, base.gotWriteDeadline, "SetWriteDeadline did not reach the base writer through statusRecorder")
}
