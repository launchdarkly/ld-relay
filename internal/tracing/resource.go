package tracing

import (
	"context"
	"log/slog"
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

// NewResource builds an OTel resource from Relay's own defaults and the standard environment
// variables (OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME). Anything the operator sets there wins over
// the defaults. If resource creation fails or returns nil, it falls back to resource.Default() and
// logs a warning.
//
// The process identity is reported as service.instance.id alone. Relay used to publish the same value
// again under a private relay.id key; that carried no information service.instance.id did not already
// carry, and Prometheus surfaces the standard attribute directly as the "instance" label.
//
// Note for Prometheus users: resource attributes are not copied onto every series. They are exposed
// through target_info, so a query that needs the process identity joins against it, or uses the
// "instance" label.
func NewResource(logger *slog.Logger) *resource.Resource {
	// resource.New merges its detectors in the order they are given, and a later value replaces an
	// earlier one. Relay's defaults therefore go first and the environment detector goes last, which
	// is what lets an operator override either default. Reading the variables here instead would mean
	// reimplementing the parsing the detector already does.
	opts := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName("ld-relay"),
			semconv.ServiceInstanceID(RelayID()),
		),
		resource.WithFromEnv(),
	}

	res, err := resource.New(context.Background(), opts...)
	if err != nil || res == nil {
		logger.Warn("failed to create OTel resource, falling back to default", "error", err)
		res = resource.Default()
	}
	return res
}
