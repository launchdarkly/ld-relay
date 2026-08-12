package tracing

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/pborman/uuid"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// relayID is generated once per process, so that the metrics and trace providers -- which each build
// their own resource -- report the same identity.
var relayID = sync.OnceValue(uuid.New) //nolint:gochecknoglobals

// RelayID returns the identifier for this relay process. It is reported as the service.instance.id
// resource attribute, and is also the ID used in the usage data Relay sends to LaunchDarkly.
func RelayID() string {
	return relayID()
}

// NewResource builds an OTel resource from standard environment variables
// (OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME). If OTEL_SERVICE_NAME is
// unset, it defaults to "ld-relay". If resource creation fails or returns
// nil, it falls back to resource.Default() and logs a warning.
//
// The process identity is reported as service.instance.id alone. Relay used to publish the same value
// again under a private relay.id key; that carried no information service.instance.id did not already
// carry, and Prometheus surfaces the standard attribute directly as the "instance" label.
//
// Note for Prometheus users: resource attributes are not copied onto every series. They are exposed
// through target_info, so a query that needs the process identity joins against it, or uses the
// "instance" label.
func NewResource(logger *slog.Logger) *resource.Resource {
	opts := []resource.Option{resource.WithFromEnv()}
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		opts = append(opts, resource.WithAttributes(semconv.ServiceName("ld-relay")))
	}
	// Only supply service.instance.id when the operator has not configured one themselves.
	if !strings.Contains(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), string(semconv.ServiceInstanceIDKey)) {
		opts = append(opts, resource.WithAttributes(semconv.ServiceInstanceID(RelayID())))
	}

	res, err := resource.New(context.Background(), opts...)
	if err != nil || res == nil {
		logger.Warn("failed to create OTel resource, falling back to default", "error", err)
		res = resource.Default()
	}
	return res
}
