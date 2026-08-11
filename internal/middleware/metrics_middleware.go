package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"

	"github.com/gorilla/mux"
)

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.written {
		sr.statusCode = code
		sr.written = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.statusCode = 200
		sr.written = true
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

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

	urlScheme := "http"
	if req.TLS != nil {
		urlScheme = "https"
	}

	// Format per OTEL semconv: "1.0", "1.1", "2", "3"
	// HTTP/2 and HTTP/3 use just the major version; HTTP/1.x includes the minor version.
	protocolVersion := ""
	if req.ProtoMajor > 0 {
		if req.ProtoMajor == 1 {
			protocolVersion = fmt.Sprintf("%d.%d", req.ProtoMajor, req.ProtoMinor)
		} else {
			protocolVersion = fmt.Sprintf("%d", req.ProtoMajor)
		}
	}

	return metrics.RequestInfo{
		UserAgent:          getUserAgent(req),
		SDKWrapper:         getSDKWrapper(req),
		Route:              route,
		Method:             req.Method,
		ApplicationID:      appID,
		ApplicationVersion: appVersion,
		URLScheme:          urlScheme,
		ProtocolVersion:    protocolVersion,
	}
}

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		ri := requestInfoFromHTTP(req)
		metrics.WithCount(env.GetMetricsEnv(), ri, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

func withStreamConnection(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		env := GetEnvContextInfo(req.Context()).Env
		ri := requestInfoFromHTTP(req)
		metrics.WithStreamConnection(env.GetMetricsEnv(), ri, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

// CountMobileConns is a middleware function that increments the number of active mobile connections
// until the handler ends.
func CountMobileConns(handler http.Handler) http.Handler {
	return withStreamConnection(handler, metrics.MobileConns)
}

// CountBrowserConns is a middleware function that increments the number of active browser connections
// until the handler ends.
func CountBrowserConns(handler http.Handler) http.Handler {
	return withStreamConnection(handler, metrics.BrowserConns)
}

// CountServerConns is a middleware function that increments the number of active server-side connections
// until the handler ends.
func CountServerConns(handler http.Handler) http.Handler {
	return withStreamConnection(handler, metrics.ServerConns)
}

// ServerPollingRequestCount is a middleware function that increments the total number of server-side polling requests.
func ServerPollingRequestCount(handler http.Handler) http.Handler {
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

// RequestMetrics is a middleware function that tracks a request in http.server.active_requests for as
// long as it is in flight, and records its duration once it completes.
//
// Active requests cover every request this middleware sees, streaming included, per the OTEL semantic
// convention for the instrument. Duration is still skipped for streaming responses, whose lifetime is
// unbounded.
//
// There is no platform-dependent variant of this: since these metrics dropped platform.category, the
// SDK platform no longer changes what gets recorded.
func RequestMetrics(endpointType metrics.EndpointType) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			env := GetEnvContextInfo(req.Context()).Env
			instruments := getInstruments(env)
			metricsEnv := env.GetMetricsEnv()
			ri := requestInfoFromHTTP(req)
			ri.EndpointType = endpointType

			requestFinished := metrics.StartActiveRequest(instruments, metricsEnv, ri)
			defer requestFinished()

			recorder := &statusRecorder{ResponseWriter: w, statusCode: 200}
			start := time.Now()
			next.ServeHTTP(recorder, req)
			// Don't record duration for streaming responses -- their lifetime is unbounded
			if !strings.HasPrefix(strings.ToLower(recorder.Header().Get("Content-Type")), "text/event-stream") {
				ri.StatusCode = recorder.statusCode
				if recorder.statusCode >= 500 {
					ri.ErrorType = fmt.Sprintf("%d", recorder.statusCode)
				}
				metrics.RecordRequestDuration(req.Context(), instruments, metricsEnv, ri, time.Since(start))
			}
		})
	}
}

// UnscopedActiveRequests tracks a request in http.server.active_requests without an LD environment, for
// endpoints that have no environment to attribute the request to. Its environment.name and
// platform.category attributes are the not-provided sentinel.
//
// This is applied directly to a handler rather than registered with Router.Use for requests that matched
// no route: gorilla/mux skips router middleware entirely when a request fails to match.
func UnscopedActiveRequests(manager *metrics.Manager, endpointType metrics.EndpointType) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ri := requestInfoFromHTTP(req)
			ri.EndpointType = endpointType

			requestFinished := metrics.StartActiveRequest(manager.GetInstruments(), manager.GetUnscopedEnvironment(), ri)
			defer requestFinished()

			next.ServeHTTP(w, req)
		})
	}
}

// EventBytesMetrics is a middleware function that records the number of event bytes received.
// This should be applied after GzipMiddleware so that it measures decompressed bytes.
func EventBytesMetrics() mux.MiddlewareFunc {
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
			ri.EndpointType = metrics.EndpointTypeEvents
			metrics.RecordEventsReceivedBytes(req.Context(), getInstruments(env), env.GetMetricsEnv(), ri, cr.bytesRead.Load())
		})
	}
}
