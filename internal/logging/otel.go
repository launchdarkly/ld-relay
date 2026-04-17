package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// OTelLogConfig holds configuration for OTel log export.
type OTelLogConfig struct {
	// Protocol is "grpc" or "http". Defaults to "grpc" if empty.
	Protocol string
}

// OTelLogProvider holds the OTel log provider and the slog handler for use with NewLogger.
type OTelLogProvider struct {
	Provider *sdklog.LoggerProvider
	Handler  slog.Handler
}

// NewOTelLogProvider creates an OTel LoggerProvider and an otelslog.Handler that can be
// passed to NewLogger via WithOTelHandler. The caller must call Shutdown on the returned
// provider when the application exits.
func NewOTelLogProvider(cfg OTelLogConfig) (*OTelLogProvider, error) {
	protocol := strings.ToLower(cfg.Protocol)
	if protocol == "" {
		protocol = "grpc"
	}

	ctx := context.Background()

	var exporter sdklog.Exporter
	var err error

	switch protocol {
	case "grpc":
		exporter, err = otlploggrpc.New(ctx)
	case "http":
		exporter, err = otlploghttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol for logs: %q (must be \"grpc\" or \"http\")", protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP %s log exporter: %w", protocol, err)
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	handler := otelslog.NewHandler("ld-relay", otelslog.WithLoggerProvider(provider))

	return &OTelLogProvider{
		Provider: provider,
		Handler:  handler,
	}, nil
}

// Shutdown gracefully shuts down the OTel log provider, flushing any pending log records.
func (p *OTelLogProvider) Shutdown(ctx context.Context) error {
	return p.Provider.Shutdown(ctx)
}
