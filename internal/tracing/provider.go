package tracing

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TracingConfig holds configuration for OTel trace export.
type TracingConfig struct {
	// Protocol is "grpc" or "http". Defaults to "grpc" if empty.
	Protocol string
}

// TracingProvider holds the OTel trace provider. The caller must call Shutdown
// when the application exits.
type TracingProvider struct {
	Provider *sdktrace.TracerProvider
}

// NewTracingProvider creates a TracerProvider with an OTLP exporter and
// registers it as the global provider. It also sets the global text-map
// propagator to support W3C Trace Context and Baggage.
func NewTracingProvider(cfg TracingConfig, logger *slog.Logger) (*TracingProvider, error) {
	exporter, err := newOTLPTraceExporter(cfg.Protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	res := NewResource(logger)

	tp := sdktrace.NewTracerProvider(
		// Wrapping the exporter, rather than adding a span processor, keeps the UTF-8 repair off the
		// request path: the batcher calls the exporter from its own goroutine.
		sdktrace.WithBatcher(NewUTF8SanitizingExporter(exporter)),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &TracingProvider{Provider: tp}, nil
}

// Shutdown gracefully shuts down the trace provider, flushing any pending spans.
func (p *TracingProvider) Shutdown(ctx context.Context) error {
	return p.Provider.Shutdown(ctx)
}
