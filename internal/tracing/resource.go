package tracing

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/pborman/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// relayIDAttrKey identifies the relay process. It is a resource attribute rather than a per-measurement
// one: it never varies within a process, so repeating it on every data point only inflated the attribute
// set that has to be built and sorted for each request.
const relayIDAttrKey = attribute.Key("relay.id")

// relayID is generated once per process, so that the metrics and trace providers -- which each build
// their own resource -- report the same identity.
var relayID = sync.OnceValue(uuid.New) //nolint:gochecknoglobals

// RelayID returns the identifier for this relay process. It is reported as the relay.id resource
// attribute, and is also the ID used in the usage data Relay sends to LaunchDarkly.
func RelayID() string {
	return relayID()
}

// NewResource builds an OTel resource from standard environment variables
// (OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME). If OTEL_SERVICE_NAME is
// unset, it defaults to "ld-relay". If resource creation fails or returns
// nil, it falls back to resource.Default() and logs a warning.
//
// Note for Prometheus users: resource attributes are not copied onto every series. They are exposed
// through target_info, so a query that needs relay.id has to join against it. service.instance.id is
// set to the same value because Prometheus surfaces that one directly as the "instance" label.
func NewResource(logger *slog.Logger) *resource.Resource {
	opts := []resource.Option{resource.WithFromEnv()}
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		opts = append(opts, resource.WithAttributes(semconv.ServiceName("ld-relay")))
	}
	attrs := []attribute.KeyValue{relayIDAttrKey.String(RelayID())}
	// Only supply service.instance.id when the operator has not configured one themselves.
	if !strings.Contains(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), string(semconv.ServiceInstanceIDKey)) {
		attrs = append(attrs, semconv.ServiceInstanceID(RelayID()))
	}
	opts = append(opts, resource.WithAttributes(attrs...))

	res, err := resource.New(context.Background(), opts...)
	if err != nil || res == nil {
		logger.Warn("failed to create OTel resource, falling back to default", "error", err)
		res = resource.Default()
	}
	return res
}
