package logging

import (
	"context"
	"errors"
	"log/slog"
)

// StderrSplitHandler routes log records to different handlers based on level.
// Records at slog.LevelError or above go to the stderr handler; all others go to stdout.
type StderrSplitHandler struct {
	stdout slog.Handler
	stderr slog.Handler
}

// NewStderrSplitHandler creates a handler that splits output between stdout and stderr handlers.
func NewStderrSplitHandler(stdout, stderr slog.Handler) *StderrSplitHandler {
	return &StderrSplitHandler{stdout: stdout, stderr: stderr}
}

func (h *StderrSplitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.stdout.Enabled(ctx, level) || h.stderr.Enabled(ctx, level)
}

func (h *StderrSplitHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		return h.stderr.Handle(ctx, r)
	}
	return h.stdout.Handle(ctx, r)
}

func (h *StderrSplitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &StderrSplitHandler{
		stdout: h.stdout.WithAttrs(attrs),
		stderr: h.stderr.WithAttrs(attrs),
	}
}

func (h *StderrSplitHandler) WithGroup(name string) slog.Handler {
	return &StderrSplitHandler{
		stdout: h.stdout.WithGroup(name),
		stderr: h.stderr.WithGroup(name),
	}
}

// MultiHandler fans out log records to multiple handlers.
// This is useful for sending logs to both console output and an OTel exporter.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a handler that dispatches to all provided handlers.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (h *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}
