package logging

import (
	"log/slog"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLDLogPrefix covers both shapes ldlog produces:
//   - Printf path: format is "LEVEL: " + format, so the rendered prefix has a space.
//   - Println path: baseLogger.Println(prefix, value) -> fmt.Sprint inserts no
//     separator between two string operands, so the rendered prefix has no space.
//
// The parser must accept both. https://github.com/launchdarkly/ld-relay/issues/670
// shipped because the parser only matched the with-space shape.
func TestParseLDLogPrefix(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantLevel   slog.Level
		wantMessage string
	}{
		{"debug no space (Println shape)", "DEBUG:querying Big Segment store metadata", slog.LevelDebug, "querying Big Segment store metadata"},
		{"debug with space (Printf shape)", "DEBUG: querying Big Segment store metadata", slog.LevelDebug, "querying Big Segment store metadata"},
		{"info no space", "INFO:Starting client", slog.LevelInfo, "Starting client"},
		{"info with space", "INFO: Starting client", slog.LevelInfo, "Starting client"},
		{"warn no space", "WARN:slow consumer", slog.LevelWarn, "slow consumer"},
		{"warn with space", "WARN: slow consumer", slog.LevelWarn, "slow consumer"},
		{"error no space", "ERROR:stream failed", slog.LevelError, "stream failed"},
		{"error with space", "ERROR: stream failed", slog.LevelError, "stream failed"},
		{"no prefix defaults to info passthrough", "starting LaunchDarkly relay", slog.LevelInfo, "starting LaunchDarkly relay"},
		{"empty defaults to info", "", slog.LevelInfo, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, msg := parseLDLogPrefix(tt.input)
			assert.Equal(t, tt.wantLevel, level)
			assert.Equal(t, tt.wantMessage, msg)
		})
	}
}

// TestBridgeRoutesLDLogCallsThroughSlog drives an ldlog.Loggers built by
// NewLDLogBridge with both Println-style (Debug/Info/Warn/Error) and
// Printf-style (Debugf/Infof/Warnf/Errorf) calls. The captured slog records
// must carry the right level and the LEVEL: prefix must be stripped from
// the message.
func TestBridgeRoutesLDLogCallsThroughSlog(t *testing.T) {
	logger, h := logtest.NewMockLogger()
	loggers := NewLDLogBridge(logger)

	loggers.Debug("debug println")
	loggers.Debugf("%s", "debug printf")
	loggers.Info("info println")
	loggers.Infof("%s", "info printf")
	loggers.Warn("warn println")
	loggers.Warnf("%s", "warn printf")
	loggers.Error("error println")
	loggers.Errorf("%s", "error printf")

	entries := h.Entries()
	require.Len(t, entries, 8)

	expected := []struct {
		level slog.Level
		msg   string
	}{
		{slog.LevelDebug, "debug println"},
		{slog.LevelDebug, "debug printf"},
		{slog.LevelInfo, "info println"},
		{slog.LevelInfo, "info printf"},
		{slog.LevelWarn, "warn println"},
		{slog.LevelWarn, "warn printf"},
		{slog.LevelError, "error println"},
		{slog.LevelError, "error printf"},
	}
	for i, want := range expected {
		assert.Equal(t, want.level, entries[i].Level, "entry %d level", i)
		assert.Equal(t, want.msg, entries[i].Message, "entry %d message", i)
	}
}

// TestBridgeRespectsSlogLevelFilter is the regression guard for
// https://github.com/launchdarkly/ld-relay/issues/670. With the slog handler
// configured at Info, SDK Debug logs that come through the bridge must be
// dropped -- not leak through as Info-level records with a "DEBUG:" prefix
// still embedded in the message field.
func TestBridgeRespectsSlogLevelFilter(t *testing.T) {
	logger, h := logtest.NewMockLogger()
	h.SetLevel(slog.LevelInfo)
	loggers := NewLDLogBridge(logger)

	loggers.Debug("querying Big Segment store metadata")
	loggers.Info("starting LaunchDarkly client")

	entries := h.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, slog.LevelInfo, entries[0].Level)
	assert.Equal(t, "starting LaunchDarkly client", entries[0].Message)
}
