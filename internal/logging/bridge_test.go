package logging

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLDLogPrefix covers the shapes the parser must accept:
//   - Printf path: format is "LEVEL: " + format, so the rendered prefix has a space.
//   - Println path: fmt.Sprintln(prefix, value) renders "LEVEL: value\n".
//   - No space after the colon, which a fmt.Sprint join produces. The parser keeps
//     accepting it; https://github.com/launchdarkly/ld-relay/issues/670 shipped
//     because the parser only matched the with-space shape.
func TestParseLDLogPrefix(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantLevel   slog.Level
		wantMessage string
	}{
		{"debug no space", "DEBUG:querying Big Segment store metadata", slog.LevelDebug, "querying Big Segment store metadata"},
		{"debug with space (Printf shape)", "DEBUG: querying Big Segment store metadata", slog.LevelDebug, "querying Big Segment store metadata"},
		{"info no space", "INFO:Starting client", slog.LevelInfo, "Starting client"},
		{"info with space", "INFO: Starting client", slog.LevelInfo, "Starting client"},
		{"info trailing newline (Println shape)", "INFO: Starting client\n", slog.LevelInfo, "Starting client"},
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

// TestBridgeSeparatesPrintlnValues checks that the bridge joins Println values
// with spaces, as log.Logger.Println does, so a SetPrefix prefix and any extra
// values stay separate from the message.
func TestBridgeSeparatesPrintlnValues(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		log       func(ldlog.Loggers)
		wantLevel slog.Level
		wantMsg   string
	}{
		{"no prefix", "", func(l ldlog.Loggers) { l.Error("stream failed") }, slog.LevelError, "stream failed"},
		{"no prefix, multiple values", "", func(l ldlog.Loggers) { l.Info("count", 3) }, slog.LevelInfo, "count 3"},
		{"prefix", "FileDataSource:", func(l ldlog.Loggers) { l.Error("Invalid key") }, slog.LevelError, "FileDataSource: Invalid key"},
		{"prefix, value and error", "FileDataSource:", func(l ldlog.Loggers) {
			l.Warn("Synchronization failed:", errors.New("HTTP error 401"))
		}, slog.LevelWarn, "FileDataSource: Synchronization failed: HTTP error 401"},
		{"prefix, Printf", "FileDataSource:", func(l ldlog.Loggers) { l.Debugf("value %d", 1) }, slog.LevelDebug, "FileDataSource: value 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, h := logtest.NewMockLogger()
			loggers := NewLDLogBridge(logger)
			if tt.prefix != "" {
				loggers.SetPrefix(tt.prefix)
			}

			tt.log(loggers)

			entries := h.Entries()
			require.Len(t, entries, 1)
			assert.Equal(t, tt.wantLevel, entries[0].Level)
			assert.Equal(t, tt.wantMsg, entries[0].Message)
		})
	}
}
