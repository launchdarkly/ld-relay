package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Option configures the logger created by NewLogger.
type Option func(*loggerConfig)

type loggerConfig struct {
	levelVar    *slog.LevelVar
	format      string
	otelHandler slog.Handler
}

// WithLevel sets the dynamic level variable for the logger.
// If not provided, a new LevelVar defaulting to slog.LevelInfo is created.
func WithLevel(lv *slog.LevelVar) Option {
	return func(c *loggerConfig) {
		c.levelVar = lv
	}
}

// WithFormat sets the output format. Valid values are "text" (default) and "json".
func WithFormat(format string) Option {
	return func(c *loggerConfig) {
		c.format = format
	}
}

// WithOTelHandler adds an OpenTelemetry slog handler that receives a copy of all log records.
// When set, log records are sent to both the console handler and the OTel handler.
func WithOTelHandler(h slog.Handler) Option {
	return func(c *loggerConfig) {
		c.otelHandler = h
	}
}

// NewLogger creates a *slog.Logger configured for ld-relay.
//
// By default it reads the LOG_FORMAT environment variable to determine format ("json" or "text").
// Error-level messages go to stderr; all others go to stdout.
// If an OTel handler is provided, log records are fanned out to both console and OTel.
func NewLogger(opts ...Option) *slog.Logger {
	cfg := &loggerConfig{
		format: os.Getenv("LOG_FORMAT"),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.levelVar == nil {
		cfg.levelVar = new(slog.LevelVar)
	}

	handlerOpts := &slog.HandlerOptions{Level: cfg.levelVar}

	var stdoutHandler, stderrHandler slog.Handler
	if strings.EqualFold(cfg.format, "json") {
		stdoutHandler = slog.NewJSONHandler(os.Stdout, handlerOpts)
		stderrHandler = slog.NewJSONHandler(os.Stderr, handlerOpts)
	} else {
		stdoutHandler = slog.NewTextHandler(os.Stdout, handlerOpts)
		stderrHandler = slog.NewTextHandler(os.Stderr, handlerOpts)
	}

	var handler slog.Handler = NewStderrSplitHandler(stdoutHandler, stderrHandler)

	if cfg.otelHandler != nil {
		handler = NewMultiHandler(handler, cfg.otelHandler)
	}

	return slog.New(handler)
}
