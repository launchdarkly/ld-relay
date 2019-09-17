package logging

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

// Global package level loggers - note that in a future version these may be replaced by ldlog.Loggers.
var (
	// Logger instance for debug-level logging from Relay (rather than from the SDK). Always non-nil.
	Debug *log.Logger
	// Logger instance for debug-level logging from Relay (rather than from the SDK). Always non-nil.
	Info *log.Logger
	// Logger instance for debug-level logging from Relay (rather than from the SDK). Always non-nil.
	Warning *log.Logger
	// Logger instance for debug-level logging from Relay (rather than from the SDK). Always non-nil.
	Error *log.Logger
)

func init() {
	InitLogging(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)
}

// InitLogging sets the destination streams for each logging level.
func InitLogging(
	debugHandle io.Writer,
	infoHandle io.Writer,
	warningHandle io.Writer,
	errorHandle io.Writer) {
	makeLog := func(w io.Writer, name string) *log.Logger {
		return log.New(w, fmt.Sprintf("%s: ", name), log.Ldate|log.Ltime|log.Lshortfile)
	}
	Debug = makeLog(debugHandle, "DEBUG")
	Info = makeLog(infoHandle, "INFO")
	Warning = makeLog(warningHandle, "WARNING")
	Error = makeLog(errorHandle, "ERROR")
}

// InitLoggingWithLevel sets up the default logger configuration based on a minimum log level.
func InitLoggingWithLevel(level ldlog.LogLevel) {
	var debugHandle io.Writer = os.Stdout
	var infoHandle io.Writer = os.Stdout
	var warningHandle io.Writer = os.Stdout
	var errorHandle io.Writer = os.Stderr
	if level > ldlog.Debug {
		debugHandle = ioutil.Discard
	}
	if level > ldlog.Info {
		infoHandle = ioutil.Discard
	}
	if level > ldlog.Warn {
		warningHandle = ioutil.Discard
	}
	if level > ldlog.Error {
		errorHandle = ioutil.Discard
	}
	InitLogging(debugHandle, infoHandle, warningHandle, errorHandle)
}

type loggingHttpResponseWriter struct {
	writer       http.ResponseWriter
	request      *http.Request
	statusCode   int
	streaming    bool
	bytesWritten uint64
}

func (w *loggingHttpResponseWriter) Header() http.Header {
	return w.writer.Header()
}

func (w *loggingHttpResponseWriter) Write(data []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(200)
	}
	w.bytesWritten += uint64(len(data))
	return w.writer.Write(data)
}

func (w *loggingHttpResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	if strings.Contains(w.writer.Header().Get("Content-Type"), "text/event-stream") {
		w.streaming = true
		w.logRequest() // for streaming requests, log at beginning of request as well as end
	} // all non-streaming requests will be logged at end of request once we know the response length
	w.writer.WriteHeader(statusCode)
}

func (w *loggingHttpResponseWriter) logRequest() {
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
			Debug.Printf("Request: method=%s url=%s auth=%s status=%d (streaming)",
				w.request.Method,
				w.request.URL,
				authStr,
				w.statusCode,
			)
		} else {
			// ending stream
			Debug.Printf("Stream closed: url=%s auth=%s bytes=%d",
				w.request.URL,
				authStr,
				w.bytesWritten,
			)
		}
	} else {
		Debug.Printf("Request: method=%s url=%s auth=%s status=%d bytes=%d",
			w.request.Method,
			w.request.URL,
			authStr,
			w.statusCode,
			w.bytesWritten,
		)
	}
}

// In order to substitute loggingHttpResponseWriter for the default http.ResponseWriter,
// it has to also implement http.Flusher and http.CloseNotifier

func (w *loggingHttpResponseWriter) Flush() {
	if f, ok := w.writer.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *loggingHttpResponseWriter) CloseNotify() <-chan bool {
	if c, ok := w.writer.(http.CloseNotifier); ok {
		return c.CloseNotify()
	}
	return make(chan bool)
}

func RequestLoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		wrappedWriter := loggingHttpResponseWriter{writer: w, request: req}
		next.ServeHTTP(&wrappedWriter, req)
		wrappedWriter.logRequest()
	})
}
