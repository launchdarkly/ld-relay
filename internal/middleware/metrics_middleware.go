package middleware

import (
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/gorilla/mux"
)

// countingReader wraps an io.ReadCloser and counts the bytes read.
type countingReader struct {
	reader    io.ReadCloser
	bytesRead atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.reader.Read(p)
	cr.bytesRead.Add(int64(n))
	return n, err
}

func (cr *countingReader) Close() error {
	return cr.reader.Close()
}

// clientPlatformCategory returns the platform category based on the credential type.
func clientPlatformCategory(cred credential.SDKCredential) string {
	if _, ok := cred.(config.MobileKey); ok {
		return metrics.MobilePlatformCategory
	}
	return metrics.BrowserPlatformCategory
}

func getInstruments(env relayenv.EnvContext) *metrics.Instruments {
	if mgr := env.GetMetricsManager(); mgr != nil {
		return mgr.GetInstruments()
	}
	return nil
}

func requestInfoFromHTTP(req *http.Request) metrics.RequestInfo {
	var route string
	if r := mux.CurrentRoute(req); r != nil {
		route, _ = r.GetPathTemplate()
	}
	appID, appVersion := parseApplicationTags(req)
	return metrics.RequestInfo{
		UserAgent:          getUserAgent(req),
		SDKWrapper:         getSDKWrapper(req),
		Route:              route,
		Method:             req.Method,
		ApplicationID:      appID,
		ApplicationVersion: appVersion,
		InstanceID:         getInstanceID(req),
	}
}

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		ri := requestInfoFromHTTP(req)
		metrics.WithCount(env.GetMetricsEnv(), getInstruments(env), ri, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

func withGauge(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		ri := requestInfoFromHTTP(req)
		metrics.WithGauge(env.GetMetricsEnv(), getInstruments(env), ri, func() {
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
	return withCount(handler, metrics.ServerPollingRequests)
}

// DynamicPollingRequestCount is a middleware function for FDv2 client-side endpoints that dynamically
// determines the polling request metric based on the credential type.
func DynamicPollingRequestCount(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cred := GetEnvContextInfo(req.Context()).Credential
		var measure metrics.Measure
		if _, ok := cred.(config.MobileKey); ok {
			measure = metrics.MobilePollingRequests
		} else {
			measure = metrics.BrowserPollingRequests
		}
		withCount(handler, measure).ServeHTTP(w, req)
	})
}

// CountClientConns is a middleware function for FDv2 client-side endpoints that dynamically
// determines whether to count as mobile or browser connections based on the credential type.
func CountClientConns(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cred := GetEnvContextInfo(req.Context()).Credential
		if _, ok := cred.(config.MobileKey); ok {
			CountMobileConns(handler).ServeHTTP(w, req)
		} else {
			CountBrowserConns(handler).ServeHTTP(w, req)
		}
	})
}

// DynamicRequestMetrics is a middleware function for FDv2 client-side endpoints that dynamically
// determines the request metrics based on the credential type.
func DynamicRequestMetrics() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			cred := GetEnvContextInfo(req.Context()).Credential
			var measure metrics.Measure
			if _, ok := cred.(config.MobileKey); ok {
				measure = metrics.MobileRequests
			} else {
				measure = metrics.BrowserRequests
			}
			RequestMetrics(measure)(next).ServeHTTP(w, req)
		})
	}
}

// RequestMetrics is a middleware function that increments the request counter
// and records the request duration for the specified metric.
func RequestMetrics(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			env := GetEnvContextInfo(req.Context()).Env
			ri := requestInfoFromHTTP(req)
			start := time.Now()
			metrics.WithRouteCount(req.Context(), env.GetMetricsEnv(), getInstruments(env), ri, func() {
				next.ServeHTTP(w, req)
			}, measure)
			if w.Header().Get("X-Accel-Buffering") != "no" {
				metrics.RecordRequestDuration(req.Context(), getInstruments(env), env.GetMetricsEnv(), ri, time.Since(start), measure)
			}
		})
	}
}

// EventBytesMetrics is a middleware function that records the number of event bytes ingested.
// This should be applied after GzipMiddleware so that it measures decompressed bytes.
func EventBytesMetrics(platformCategory string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Body == nil || req.Body == http.NoBody {
				next.ServeHTTP(w, req)
				return
			}
			cr := &countingReader{reader: req.Body}
			req.Body = cr
			next.ServeHTTP(w, req)
			env := GetEnvContextInfo(req.Context()).Env
			ri := requestInfoFromHTTP(req)
			metrics.RecordEventsIngestedBytes(req.Context(), getInstruments(env), env.GetMetricsEnv(), platformCategory, ri, cr.bytesRead.Load())
		})
	}
}
