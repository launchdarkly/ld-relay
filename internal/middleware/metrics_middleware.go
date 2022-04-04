package middleware

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/internal/metrics"

	"github.com/gorilla/mux"
)

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context()).Env
		userAgent := getUserAgent(req)
		metrics.WithCount(ctx.GetMetricsContext(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

func withGauge(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context())
		userAgent := getUserAgent(req)
		metrics.WithGauge(ctx.Env.GetMetricsContext(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

// CountMobileConns is a middleware function that increments the total number of mobile connections,
// and also increments the number of active mobile connections until the handler ends.
func CountMobileConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.MobileConns), metrics.NewMobileConns)
}

// CountBrowserConns is a middleware function that increments the total number of browser connections,
// and also increments the number of active browser connections until the handler ends.
func CountBrowserConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.BrowserConns), metrics.NewBrowserConns)
}

// CountServerConns is a middleware function that increments the total number of server-side connections,
// and also increments the number of active server-side connections until the handler ends.
func CountServerConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.ServerConns), metrics.NewServerConns)
}

// RequestCount is a middleware function that increments the specified metric for each request.
func RequestCount(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			// Ignoring internal routing error that would have been ignored anyway
			route, _ := mux.CurrentRoute(req).GetPathTemplate()
			metrics.WithRouteCount(ctx.Env.GetMetricsContext(), userAgent, route, req.Method, func() {
				next.ServeHTTP(w, req)
			}, measure)
		})
	}
}
