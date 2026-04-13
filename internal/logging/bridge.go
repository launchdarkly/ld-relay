package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

// slogBaseLogger implements ldlog.BaseLogger by delegating to a *slog.Logger.
// The ldlog library prepends level prefixes ("DEBUG: ", "INFO: ", etc.) to messages
// before calling Println/Printf, so we parse those back out to determine the slog level.
type slogBaseLogger struct {
	logger *slog.Logger
}

func (b *slogBaseLogger) Println(values ...interface{}) {
	msg := fmt.Sprint(values...)
	level, cleanMsg := parseLDLogPrefix(msg)
	b.logger.Log(context.Background(), level, cleanMsg)
}

func (b *slogBaseLogger) Printf(format string, values ...interface{}) {
	msg := fmt.Sprintf(format, values...)
	level, cleanMsg := parseLDLogPrefix(msg)
	b.logger.Log(context.Background(), level, cleanMsg)
}

// parseLDLogPrefix extracts the log level from an ldlog-formatted message.
// ldlog prepends "DEBUG: ", "INFO: ", "WARN: ", or "ERROR: " to messages.
func parseLDLogPrefix(message string) (slog.Level, string) {
	prefixes := []struct {
		prefix string
		level  slog.Level
	}{
		{"DEBUG: ", slog.LevelDebug},
		{"INFO: ", slog.LevelInfo},
		{"WARN: ", slog.LevelWarn},
		{"ERROR: ", slog.LevelError},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(message, p.prefix) {
			return p.level, strings.TrimSpace(strings.TrimPrefix(message, p.prefix))
		}
	}
	return slog.LevelInfo, strings.TrimSpace(message)
}

// NewLDLogBridge creates an ldlog.Loggers instance that delegates all logging to the given
// slog.Logger. This is used to configure the LaunchDarkly SDK, which requires ldlog.Loggers.
//
// The bridge sets ldlog's minimum level to Debug so that all filtering is controlled by
// slog's handler. This avoids double-filtering between ldlog and slog.
func NewLDLogBridge(logger *slog.Logger) ldlog.Loggers {
	loggers := ldlog.NewDefaultLoggers()
	bridge := &slogBaseLogger{logger: logger}
	loggers.SetBaseLogger(bridge)
	loggers.SetMinLevel(ldlog.Debug)
	return loggers
}
