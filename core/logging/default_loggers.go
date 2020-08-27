package logging

import (
	"io"
	"log"
	"os"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// MakeDefaultLoggers returns a Loggers instance configured with Relay's standard log format.
// Output goes to stdout, except Error level which goes to stderr. Debug level is disabled.
func MakeDefaultLoggers() ldlog.Loggers {
	loggers := ldlog.NewDefaultLoggers()
	loggers.SetBaseLogger(makeLog(os.Stdout))
	loggers.SetBaseLoggerForLevel(ldlog.Error, makeLog(os.Stderr))
	loggers.SetMinLevel(ldlog.Info)
	return loggers
}

func makeLog(w io.Writer) *log.Logger {
	return log.New(w, "", log.Ldate|log.Ltime|log.Lmicroseconds)
}
