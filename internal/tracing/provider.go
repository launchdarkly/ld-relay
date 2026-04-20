package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
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

	resourceOpts := []resource.Option{resource.WithFromEnv()}
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		resourceOpts = append(resourceOpts, resource.WithAttributes(semconv.ServiceName("ld-relay")))
	}
	res, resErr := resource.New(context.Background(), resourceOpts...)
	if resErr != nil || res == nil {
		logger.Warn("failed to create OTel resource, falling back to default", "error", resErr)
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
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
