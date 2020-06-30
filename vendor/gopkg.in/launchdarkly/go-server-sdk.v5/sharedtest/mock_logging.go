//nolint:gochecknoglobals,golint,stylecheck
package sharedtest

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var testLogLevel = ldlog.None

// NewTestLoggers returns a standardized logger instance used by unit tests. If you want to temporarily
// enable log output for tests, change testLogLevel to for instance ldlog.Debug. Note that "go test"
// normally suppresses output anyway unless a test fails.
func NewTestLoggers() ldlog.Loggers {
	ret := ldlog.NewDefaultLoggers()
	ret.SetMinLevel(testLogLevel)
	return ret
}

// MockLogItem represents a log message captured by MockLoggers.
type MockLogItem struct {
	Level   ldlog.LogLevel
	Message string
}

// MockLoggers provides the ability to capture log output.
type MockLoggers struct {
	// Loggers is the ldlog.Loggers instance to be used for tests.
	Loggers ldlog.Loggers
	// Output is a map containing all of the lines logged for each level. The level prefix is removed from the text.
	output map[ldlog.LogLevel][]string
	// AllOutput is a list of all the log output for any level in order. The level prefix is removed from the text.
	allOutput []MockLogItem
	lock      sync.Mutex
}

// NullLoggers returns a Loggers instance that suppresses all output.
func NullLoggers() ldlog.Loggers {
	ret := ldlog.Loggers{}
	ret.SetMinLevel(ldlog.None)
	return ret
}

// NewMockLoggers creates a log-capturing object.
func NewMockLoggers() *MockLoggers {
	ret := &MockLoggers{output: make(map[ldlog.LogLevel][]string)}
	for _, level := range []ldlog.LogLevel{ldlog.Debug, ldlog.Info, ldlog.Warn, ldlog.Error} {
		ret.Loggers.SetBaseLoggerForLevel(level, mockBaseLogger{owner: ret, level: level})
	}
	return ret
}

// GetOutput returns the captured output for a specific log level.
func (ml *MockLoggers) GetOutput(level ldlog.LogLevel) []string {
	ml.lock.Lock()
	defer ml.lock.Unlock()
	lines := ml.output[level]
	ret := make([]string, len(lines))
	copy(ret, lines)
	return ret
}

// GetAllOutput returns the captured output for all log levels.
func (ml *MockLoggers) GetAllOutput() []MockLogItem {
	ml.lock.Lock()
	defer ml.lock.Unlock()
	ret := make([]MockLogItem, len(ml.allOutput))
	copy(ret, ml.allOutput)
	return ret
}

func (ml *MockLoggers) logLine(level ldlog.LogLevel, line string) {
	ml.lock.Lock()
	defer ml.lock.Unlock()
	message := strings.TrimPrefix(line, strings.ToUpper(level.String())+": ")
	ml.output[level] = append(ml.output[level], message)
	ml.allOutput = append(ml.allOutput, MockLogItem{level, message})
}

type mockBaseLogger struct {
	owner *MockLoggers
	level ldlog.LogLevel
}

func (l mockBaseLogger) Println(values ...interface{}) {
	l.owner.logLine(l.level, strings.TrimSuffix(fmt.Sprintln(values...), "\n"))
}

func (l mockBaseLogger) Printf(format string, values ...interface{}) {
	l.owner.logLine(l.level, fmt.Sprintf(format, values...))
}
