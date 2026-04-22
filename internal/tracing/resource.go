package tracing

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// NewResource builds an OTel resource from standard environment variables
// (OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME). If OTEL_SERVICE_NAME is
// unset, it defaults to "ld-relay". If resource creation fails or returns
// nil, it falls back to resource.Default() and logs a warning.
func NewResource(logger *slog.Logger) *resource.Resource {
	opts := []resource.Option{resource.WithFromEnv()}
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		opts = append(opts, resource.WithAttributes(semconv.ServiceName("ld-relay")))
	}
	res, err := resource.New(context.Background(), opts...)
	if err != nil || res == nil {
		logger.Warn("failed to create OTel resource, falling back to default", "error", err)
		res = resource.Default()
	}
	return res
}
