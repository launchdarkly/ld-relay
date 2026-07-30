package logging

import (
	"log/slog"
	"net/http"
	"strings"
)

// RequestLoggerMiddleware decorates a Handler with debug-level logging of all requests.
func RequestLoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			wrappedWriter := loggingHTTPResponseWriter{logger: logger, writer: w, request: req}
			next.ServeHTTP(&wrappedWriter, req)
			wrappedWriter.logRequest()
		})
	}
}

type loggingHTTPResponseWriter struct {
	logger       *slog.Logger
	writer       http.ResponseWriter
	request      *http.Request
	statusCode   int
	streaming    bool
	bytesWritten uint64
}

func (w *loggingHTTPResponseWriter) Header() http.Header {
	return w.writer.Header()
}

func (w *loggingHTTPResponseWriter) Write(data []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(200)
	}
	w.bytesWritten += uint64(len(data))
	return w.writer.Write(data)
}

func (w *loggingHTTPResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	if strings.Contains(w.writer.Header().Get("Content-Type"), "text/event-stream") {
		w.streaming = true
		w.logRequest() // for streaming requests, log at beginning of request as well as end
	} // all non-streaming requests will be logged at end of request once we know the response length
	w.writer.WriteHeader(statusCode)
}

func (w *loggingHTTPResponseWriter) logRequest() {
	authStr := "n/a"
	if authHeader := w.request.Header.Get("Authorization"); authHeader != "" {
		if len(authHeader) > 5 {
			authStr = "*" + authHeader[len(authHeader)-5:]
		} else {
			authStr = authHeader
		}
	}
	if w.streaming {
		if w.bytesWritten == 0 {
			// starting stream
			w.logger.Debug("request started",
				"method", w.request.Method,
				"url", w.request.URL.String(),
				"auth", authStr,
				"status", w.statusCode,
				"streaming", true,
			)
		} else {
			// ending stream
			w.logger.Debug("stream closed",
				"url", w.request.URL.String(),
				"auth", authStr,
				"bytes", w.bytesWritten,
			)
		}
	} else {
		w.logger.Debug("request completed",
			"method", w.request.Method,
			"url", w.request.URL.String(),
			"auth", authStr,
			"status", w.statusCode,
			"bytes", w.bytesWritten,
		)
	}
}

// In order to substitute loggingHTTPResponseWriter for the default http.ResponseWriter,
// it has to also implement http.Flusher

func (w *loggingHTTPResponseWriter) Flush() {
	if f, ok := w.writer.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped ResponseWriter so that http.NewResponseController can reach the
// underlying connection (e.g. to set read/write deadlines) through this logging wrapper.
func (w *loggingHTTPResponseWriter) Unwrap() http.ResponseWriter {
	return w.writer
}
