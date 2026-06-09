package logging

import (
	"log/slog"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
)

// LevelNone is a slog.Level that is higher than all standard levels,
// effectively disabling all logging when set as the minimum level.
const LevelNone = slog.Level(12)

// SlogLevelFromString parses a log level name (case-insensitive) into a slog.Level.
// Valid values are "debug", "info", "warn", "error", and "none".
// Returns the level and true if the string was recognized, or slog.LevelInfo and false otherwise.
func SlogLevelFromString(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	case "none":
		return LevelNone, true
	default:
		return slog.LevelInfo, false
	}
}

// SlogLevelFromLDLog converts an ldlog.LogLevel to the equivalent slog.Level.
func SlogLevelFromLDLog(level ldlog.LogLevel) slog.Level {
	switch level {
	case ldlog.Debug:
		return slog.LevelDebug
	case ldlog.Info:
		return slog.LevelInfo
	case ldlog.Warn:
		return slog.LevelWarn
	case ldlog.Error:
		return slog.LevelError
	case ldlog.None:
		return LevelNone
	default:
		return slog.LevelInfo
	}
}

// LDLogLevelFromSlog converts a slog.Level to the equivalent ldlog.LogLevel.
func LDLogLevelFromSlog(level slog.Level) ldlog.LogLevel {
	switch {
	case level <= slog.LevelDebug:
		return ldlog.Debug
	case level <= slog.LevelInfo:
		return ldlog.Info
	case level <= slog.LevelWarn:
		return ldlog.Warn
	case level <= slog.LevelError:
		return ldlog.Error
	default:
		return ldlog.None
	}
}
