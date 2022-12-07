package bigsegments

import (
	"os"

	"github.com/launchdarkly/go-configtypes"
)

// See "Experimental/testing variables" in configuration.md. We use LD_TRACE_LOG_BIG_SEGMENTS in our
// integration tests to get the most detailed diagnostic output possible. We capture this setting in
// a global variable at startup time so we don't incur the overhead of re-parsing the environment
// value repeatedly in big segments logic.

var enableTraceLogging = getEnableTraceLogging() //nolint:gochecknoglobals

func getEnableTraceLogging() bool {
	value, _ := configtypes.NewOptBoolFromString(os.Getenv("LD_TRACE_LOG_BIG_SEGMENTS"))
	return value.GetOrElse(false)
}
