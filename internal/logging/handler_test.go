package logging

import (
	"context"
	"log/slog"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/stretchr/testify/assert"
)

func TestLevelHandlerDropsRecordsBelowItsLevel(t *testing.T) {
	mock := logtest.NewMockHandler()
	logger := slog.New(NewLevelHandler(mock, slog.LevelWarn))

	logger.Info("info message")
	logger.Warn("warn message")

	assert.Equal(t, []string{"warn message"}, mock.AllMessages())
}

func TestLevelHandlerFollowsChangesToALevelVar(t *testing.T) {
	mock := logtest.NewMockHandler()
	levelVar := new(slog.LevelVar)
	logger := slog.New(NewLevelHandler(mock, levelVar))

	logger.Debug("before")
	levelVar.Set(slog.LevelDebug)
	logger.Debug("after")

	assert.Equal(t, []string{"after"}, mock.AllMessages())
}

func TestLevelHandlerKeepsItsLevelAfterWith(t *testing.T) {
	mock := logtest.NewMockHandler()
	logger := slog.New(NewLevelHandler(mock, slog.LevelWarn)).With("env", "x").WithGroup("g")

	logger.Info("info message")
	logger.Warn("warn message")

	assert.Equal(t, []string{"warn message"}, mock.AllMessages())
}

func TestWithMinLevelCanLowerTheLevel(t *testing.T) {
	mock := logtest.NewMockHandler()
	base := slog.New(NewLevelHandler(mock, slog.LevelInfo))
	derived := WithMinLevel(base.With("env", "x"), slog.LevelDebug)

	base.Debug("base debug")
	derived.Debug("derived debug")

	assert.Equal(t, []string{"derived debug"}, mock.AllMessages())
	assert.Equal(t, "x", mock.Entries()[0].Attrs["env"])
}

func TestWithMinLevelCanRaiseTheLevel(t *testing.T) {
	mock := logtest.NewMockHandler()
	base := slog.New(NewLevelHandler(mock, slog.LevelDebug))
	derived := WithMinLevel(base, slog.LevelError)

	base.Info("base info")
	derived.Warn("derived warn")
	derived.Error("derived error")

	assert.Equal(t, []string{"base info", "derived error"}, mock.AllMessages())
}

func TestWithMinLevelDoesNotLowerTheLevelOfAnotherHandler(t *testing.T) {
	mock := logtest.NewMockHandler()
	mock.SetLevel(slog.LevelInfo)
	derived := WithMinLevel(slog.New(mock), slog.LevelDebug)

	derived.Debug("derived debug")
	derived.Info("derived info")

	assert.Equal(t, []string{"derived info"}, mock.AllMessages())
}

func TestNewLoggerUsesTheLevelVar(t *testing.T) {
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelWarn)
	logger := NewLogger(WithLevel(levelVar))

	assert.False(t, logger.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, logger.Enabled(context.Background(), slog.LevelWarn))
	assert.True(t, WithMinLevel(logger, slog.LevelDebug).Enabled(context.Background(), slog.LevelDebug))
}
