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

// Global logging object for Relay messages that are not tied to a specific environment.
// Use level-specific output methods such as Info/Infof, Warn/Warnf, tc.
//
// Output sent here does not have an environment name prefix, and is filtered by
// the logLevel/LOG_LEVEL parameter rather than by envLogLevel/ENV_LOG_LEVEL.
//
// The obsolete variables logging.Debug, logging.Info, etc. will still work, and will respect the
// log level configuration in terms of filtering, but will not include the level name in the
// output.
var GlobalLoggers ldlog.Loggers

// This is set to true if the application explicitly called InitLogging
var initializedWithSpecificWriters bool

// Global package level loggers - these are preserved for backward compatibility, but in a
// future version they will be replaced by ldlog.Loggers.
// Output sent to these loggers does not have an environment name prefix, and is filtered by
// the logLevel/LOG_LEVEL parameter rather than by envLogLevel/ENV_LOG_LEVEL.
var (
	// Obsolete Logger instance for global debug-level logging. Retained for backward compatibility.
	// Deprecated: Use GlobalLoggers.
	Debug *log.Logger
	// Obsolete Logger instance for global info-level logging. Retained for backward compatibility.
	// Deprecated: Use GlobalLoggers.
	Info *log.Logger
	// Obsolete Logger instance for global warn-level logging. Retained for backward compatibility.
	// Deprecated: Use GlobalLoggers.
	Warning *log.Logger
	// Obsolete Logger instance for global error-level logging. Retained for backward compatibility.
	// Deprecated: Use GlobalLoggers.
	Error *log.Logger
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
	Debug = makeLog(debugHandle)
	Info = makeLog(infoHandle)
	Warning = makeLog(warningHandle)
	Error = makeLog(errorHandle)
	GlobalLoggers = MakeLoggers("main")
}

func makeLog(w io.Writer) *log.Logger {
	return log.New(w, "", log.Ldate|log.Ltime|log.Lmicroseconds)
}

// MakeLoggers returns a ldlog.Loggers instance that uses the previously configured log writers,
// with an optional category description that will be prepended to messages.
func MakeLoggers(category string) ldlog.Loggers {
	loggers := ldlog.Loggers{}
	loggers.SetBaseLoggerForLevel(ldlog.Debug, Debug)
	loggers.SetBaseLoggerForLevel(ldlog.Info, Info)
	loggers.SetBaseLoggerForLevel(ldlog.Warn, Warning)
	loggers.SetBaseLoggerForLevel(ldlog.Error, Error)
	if category != "" {
		loggers.SetPrefix(fmt.Sprintf("[%s]", category))
	}
	return loggers
}

// InitLoggingWithLevel sets up the default logger configuration based on a minimum log level.
// This is the preferred method rather than InitLogging.
func InitLoggingWithLevel(level ldlog.LogLevel) {
	if initializedWithSpecificWriters {
		// The application called InitLogging directly, so we don't want to recreate the Logger
		// instances - just disable the ones that should be disabled.
		nullLog := makeLog(ioutil.Discard)
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
	GlobalLoggers.SetMinLevel(level)
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
			GlobalLoggers.Debugf("Request: method=%s url=%s auth=%s status=%d (streaming)",
				w.request.Method,
				w.request.URL,
				authStr,
				w.statusCode,
			)
		} else {
			// ending stream
			GlobalLoggers.Debugf("Stream closed: url=%s auth=%s bytes=%d",
				w.request.URL,
				authStr,
				w.bytesWritten,
			)
		}
	} else {
		GlobalLoggers.Debugf("Request: method=%s url=%s auth=%s status=%d bytes=%d",
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
