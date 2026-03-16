package middleware

import (
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/gorilla/mux"
)

func getInstruments(env relayenv.EnvContext) *metrics.Instruments {
	if mgr := env.GetMetricsManager(); mgr != nil {
		return mgr.GetInstruments()
	}
	return nil
}

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		userAgent := getUserAgent(req)
		sdkWrapper := getSDKWrapper(req)
		metrics.WithCount(env.GetMetricsEnv(), getInstruments(env), userAgent, sdkWrapper, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

func withGauge(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		userAgent := getUserAgent(req)
		sdkWrapper := getSDKWrapper(req)
		metrics.WithGauge(env.GetMetricsEnv(), getInstruments(env), userAgent, sdkWrapper, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

// CountMobileConns is a middleware function that increments the number of active mobile connections
// until the handler ends.
func CountMobileConns(handler http.Handler) http.Handler {
	return withGauge(handler, metrics.MobileConns)
}

// CountBrowserConns is a middleware function that increments the number of active browser connections
// until the handler ends.
func CountBrowserConns(handler http.Handler) http.Handler {
	return withGauge(handler, metrics.BrowserConns)
}

// CountServerConns is a middleware function that increments the number of active server-side connections
// until the handler ends.
func CountServerConns(handler http.Handler) http.Handler {
	return withGauge(handler, metrics.ServerConns)
}

// PollingRequestCount is a middleware function that increments the total number of server-side polling requests.
func PollingRequestCount(handler http.Handler) http.Handler {
	return withCount(handler, metrics.PollingRequests)
}

// RequestMetrics is a middleware function that increments the request counter
// and records the request duration for the specified metric.
func RequestMetrics(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			env := GetEnvContextInfo(req.Context()).Env
			userAgent := getUserAgent(req)
			sdkWrapper := getSDKWrapper(req)
			var route string
			if r := mux.CurrentRoute(req); r != nil {
				route, _ = r.GetPathTemplate()
			}
			start := time.Now()
			metrics.WithRouteCount(req.Context(), env.GetMetricsEnv(), getInstruments(env), userAgent, sdkWrapper, route, req.Method, func() {
				next.ServeHTTP(w, req)
			}, measure)
			metrics.RecordRequestDuration(req.Context(), getInstruments(env), env.GetMetricsEnv(), userAgent, sdkWrapper, route, req.Method, time.Since(start), measure)
		})
	}
}
