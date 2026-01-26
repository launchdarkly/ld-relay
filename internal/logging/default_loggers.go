package logging

import (
	"io"
	"log"
	"os"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

// MakeDefaultLoggers returns a Loggers instance configured with Relay's standard log format.
// Output goes to stdout, except Error level which goes to stderr. Debug level is disabled.
//
// The log format is determined by the LOG_FORMAT environment variable:
//   - "text" (default): traditional text-based log format with timestamps
//   - "json": JSON-structured log output
func MakeDefaultLoggers() ldlog.Loggers {
	loggers := ldlog.NewDefaultLoggers()

	if isJSONFormat() {
		loggers.SetBaseLogger(NewJSONLogger(os.Stdout))
		loggers.SetBaseLoggerForLevel(ldlog.Error, NewJSONLogger(os.Stderr))
	} else {
		loggers.SetBaseLogger(makeTextLog(os.Stdout))
		loggers.SetBaseLoggerForLevel(ldlog.Error, makeTextLog(os.Stderr))
	}

	loggers.SetMinLevel(ldlog.Info)
	return loggers
}

// isJSONFormat checks if LOG_FORMAT environment variable is set to "json".
func isJSONFormat() bool {
	format := os.Getenv("LOG_FORMAT")
	return strings.EqualFold(format, "json")
}

func makeTextLog(w io.Writer) *log.Logger {
	return log.New(w, "", log.Ldate|log.Ltime|log.Lmicroseconds)
}
