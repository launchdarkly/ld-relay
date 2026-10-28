package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newOTLPTraceExporter(protocol string) (sdktrace.SpanExporter, error) {
	ctx := context.Background()

	switch protocol {
	case "grpc":
		return otlptracegrpc.New(ctx)
	case "http/protobuf":
		return otlptracehttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol for traces: %q (must be \"grpc\" or \"http/protobuf\")", protocol)
	}
}
