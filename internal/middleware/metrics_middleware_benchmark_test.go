package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/gorilla/mux"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// durationOnlyMetrics reconstructs the pre-change middleware: status recording, timing, and the
// duration histogram, with no active-request tracking. It exists solely so the benchmark below can
// price the active-request half against a like-for-like baseline in a single tree, without checking
// out the parent commit.
func durationOnlyMetrics(measure metrics.Measure, endpointType metrics.EndpointType) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			env := GetEnvContextInfo(req.Context()).Env
			ri := requestInfoFromHTTP(req)
			ri.EndpointType = endpointType
			recorder := &statusRecorder{ResponseWriter: w, statusCode: 200}
			start := time.Now()
			next.ServeHTTP(recorder, req)
			if !strings.HasPrefix(strings.ToLower(recorder.Header().Get("Content-Type")), "text/event-stream") {
				ri.StatusCode = recorder.statusCode
				if recorder.statusCode >= 500 {
					ri.ErrorType = fmt.Sprintf("%d", recorder.statusCode)
				}
				metrics.RecordRequestDuration(req.Context(), getInstruments(env), env.GetMetricsEnv(), ri, time.Since(start), measure)
			}
		})
	}
}

// discardWriter is a minimal ResponseWriter so the benchmark measures the middleware rather than
// httptest.NewRecorder's per-call buffer allocation.
type discardWriter struct{ header http.Header }

func (d *discardWriter) Header() http.Header         { return d.header }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

func benchmarkEnv(b *testing.B, realMeter bool) relayenv.EnvContext {
	b.Helper()
	manager, err := metrics.NewManager(config.OpenTelemetryConfig{}, time.Minute, slog.New(slog.DiscardHandler))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(manager.Close)

	if realMeter {
		// A fresh reader per call: reusing one across sub-benchmarks fails to register on the second
		// provider ("duplicate reader registration"), which silently turns the real-meter case into a
		// no-aggregation one.
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
		instruments, err := metrics.NewInstrumentsForTest(provider.Meter("ld-relay"))
		if err != nil {
			b.Fatal(err)
		}
		manager.SetInstrumentsForTest(instruments)
	}

	env, err := relayenv.NewEnvContext(relayenv.EnvContextImplParams{
		Identifiers:    relayenv.EnvIdentifiers{ConfiguredName: "benchenv"},
		EnvConfig:      config.EnvConfig{},
		AllConfig:      config.Config{},
		ClientFactory:  testclient.FakeLDClientFactory(true),
		MetricsManager: manager,
		LogNameMode:    relayenv.LogNameIsEnvID,
		Logger:         slog.New(slog.DiscardHandler),
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = env.Close() })
	return env
}

// BenchmarkRequestMetricsMiddleware prices the middleware that every non-streaming request passes
// through, comparing the shipped version (active requests + duration) against a reconstruction of the
// pre-change version (duration only). The delta is what broadening
// http.server.active_requests beyond streams costs per request.
//
// "real meter" is the OTLP-enabled case. "noop meter" is the OpenTelemetry-disabled case, which still
// builds attribute sets, so it shows what operators who export nothing now pay.
func BenchmarkRequestMetricsMiddleware(b *testing.B) {
	specs := []struct {
		name       string
		middleware mux.MiddlewareFunc
	}{
		{name: "active+duration", middleware: RequestMetrics(metrics.ServerDuration, metrics.EndpointTypePoll)},
		{name: "duration-only (pre-change)", middleware: durationOnlyMetrics(metrics.ServerDuration, metrics.EndpointTypePoll)},
	}

	for _, meterCase := range []struct {
		name string
		real bool
	}{
		{name: "real meter", real: true},
		{name: "noop meter", real: false},
	} {
		for _, spec := range specs {
			b.Run(meterCase.name+"/"+spec.name, func(b *testing.B) {
				env := benchmarkEnv(b, meterCase.real)

				router := mux.NewRouter()
				router.Use(spec.middleware)
				router.Handle("/sdk/evalx/{envId}/contexts/{context}",
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusOK)
					})).Methods("GET")

				req, _ := http.NewRequest("GET", "/sdk/evalx/env-id/contexts/ctx", nil)
				req.Header.Set("User-Agent", "GoClient/7.15.4")
				req.Header.Set("X-LaunchDarkly-Tags", "application-id/my-app application-version/1.2.3")
				req.Header.Set("X-LaunchDarkly-Instance-Id", "instance-abc")
				req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: env}))

				w := &discardWriter{header: make(http.Header)}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					router.ServeHTTP(w, req)
				}
			})
		}
	}
}
