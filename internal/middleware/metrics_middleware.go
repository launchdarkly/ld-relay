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

// Unwrap exposes the wrapped ResponseWriter so that http.NewResponseController can reach
// the underlying connection (e.g. to set read/write deadlines). Without this, a controller
// built on top of this recorder silently loses those capabilities.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
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
		InstanceID:         getInstanceID(req),
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

// DynamicDurationMetrics is a middleware function for FDv2 client-side endpoints that dynamically
// determines the duration metric platform based on the credential type.
func DynamicDurationMetrics() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			cred := GetEnvContextInfo(req.Context()).Credential
			var measure metrics.Measure
			if _, ok := cred.(config.MobileKey); ok {
				measure = metrics.MobileDuration
			} else {
				measure = metrics.BrowserDuration
			}
			DurationMetrics(measure)(next).ServeHTTP(w, req)
		})
	}
}

// DurationMetrics is a middleware function that records the request duration for the specified metric.
func DurationMetrics(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			env := GetEnvContextInfo(req.Context()).Env
			ri := requestInfoFromHTTP(req)
			recorder := &statusRecorder{ResponseWriter: w, statusCode: 200}
			start := time.Now()
			next.ServeHTTP(recorder, req)
			// Don't record duration for streaming responses -- their lifetime is unbounded
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

// EventBytesMetrics is a middleware function that records the number of event bytes received.
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
			metrics.RecordEventsReceivedBytes(req.Context(), getInstruments(env), env.GetMetricsEnv(), platformCategory, ri, cr.bytesRead.Load())
		})
	}
}
