package logging

import (
	"context"
	"fmt"
	"log/slog"
)

// slogEventSourceLogger adapts a *slog.Logger to the eventsource.Logger interface
// (which requires Println and Printf methods). All messages are logged at Info level.
type slogEventSourceLogger struct {
	logger *slog.Logger
}

// NewEventSourceLogger creates an eventsource-compatible logger that delegates to
// the given slog.Logger. The eventsource library uses Println/Printf for stream
// connection lifecycle messages, which are logged at Info level.
func NewEventSourceLogger(logger *slog.Logger) interface {
	Println(...interface{})
	Printf(string, ...interface{})
} {
	return &slogEventSourceLogger{logger: logger}
}

func (l *slogEventSourceLogger) Println(values ...interface{}) {
	l.logger.Log(context.Background(), slog.LevelInfo, fmt.Sprint(values...))
}

func (l *slogEventSourceLogger) Printf(format string, values ...interface{}) {
	l.logger.Log(context.Background(), slog.LevelInfo, fmt.Sprintf(format, values...))
}
