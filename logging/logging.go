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

// Global package level loggers - these are preserved for backward compatibility, but in a
// future version they will be replaced by ldlog.Loggers.
// Output sent to these loggers does not have an environment name prefix, and is filtered by
// the logLevel/LOG_LEVEL parameter rather than by envLogLevel/ENV_LOG_LEVEL.
var (
	// Logger instance for global (not per-environment) debug-level logging. Always non-nil.
	Debug *log.Logger
	// Logger instance for global (not per-environment) info-level logging. Always non-nil.
	Info *log.Logger
	// Logger instance for global (not per-environment) warn-level logging. Always non-nil.
	Warning *log.Logger
	// Logger instance for global (not per-environment) error-level logging. Always non-nil.
	Error *log.Logger
	// This is set to true if the application explicitly called InitLogging
	initializedWithSpecificWriters bool
)

func init() {
	initLoggingInternal(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)
}

// InitLogging sets the destination streams for each logging level.
func InitLogging(
	debugHandle io.Writer,
	infoHandle io.Writer,
	warningHandle io.Writer,
	errorHandle io.Writer) {
	initLoggingInternal(debugHandle, infoHandle, warningHandle, errorHandle)
	initializedWithSpecificWriters = true
}

func initLoggingInternal(
	debugHandle io.Writer,
	infoHandle io.Writer,
	warningHandle io.Writer,
	errorHandle io.Writer) {
	Debug = makeLog(debugHandle, "DEBUG")
	Info = makeLog(infoHandle, "INFO")
	Warning = makeLog(warningHandle, "WARNING")
	Error = makeLog(errorHandle, "ERROR")
}

func makeLog(w io.Writer, name string) *log.Logger {
	return log.New(w, fmt.Sprintf("%s: ", name), log.Ldate|log.Ltime|log.Lshortfile)
}

// InitLoggingWithLevel sets up the default logger configuration based on a minimum log level.
// This is the preferred method rather than InitLogging.
func InitLoggingWithLevel(level ldlog.LogLevel) {
	if initializedWithSpecificWriters {
		// The application called InitLogging directly, so we don't want to recreate the Logger
		// instances - just disable the ones that should be disabled.
		nullLog := makeLog(ioutil.Discard, "")
		if level > ldlog.Debug {
			Debug = nullLog
		}
		if level > ldlog.Info {
			Info = nullLog
		}
		if level > ldlog.Warn {
			Warning = nullLog
		}
		if level > ldlog.Error {
			Error = nullLog
		}
		return
	}

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
	if c, ok := w.writer.(http.CloseNotifier); ok { //nolint
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
