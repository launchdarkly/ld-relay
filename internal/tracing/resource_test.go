package tracing

import (
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The process identity is reported once, as service.instance.id. Relay used to publish the same value
// again under a private relay.id key, which carried nothing the standard attribute did not.
func TestResourceReportsProcessIdentityAsServiceInstanceID(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	instanceID, ok := res.Set().Value(attribute.Key("service.instance.id"))
	require.True(t, ok, "service.instance.id not present on the resource")
	assert.Equal(t, RelayID(), instanceID.AsString())

	_, ok = res.Set().Value(attribute.Key("relay.id"))
	assert.False(t, ok, "relay.id duplicated service.instance.id and should no longer be reported")

	serviceName, ok := res.Set().Value(attribute.Key("service.name"))
	require.True(t, ok, "service.name not present on the resource")
	assert.Equal(t, "ld-relay", serviceName.AsString())
}

// An operator who sets service.instance.id themselves owns it, so relay must not overwrite it with its
// own generated value.
func TestResourceKeepsAnOperatorSuppliedServiceInstanceID(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=pod-7")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	instanceID, ok := res.Set().Value(attribute.Key("service.instance.id"))
	require.True(t, ok, "service.instance.id not present on the resource")
	assert.Equal(t, "pod-7", instanceID.AsString())
	assert.NotEqual(t, RelayID(), instanceID.AsString())
}

// OTEL_SERVICE_NAME wins over the built-in default.
func TestResourceUsesTheConfiguredServiceName(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "my-relay")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	serviceName, ok := res.Set().Value(attribute.Key("service.name"))
	require.True(t, ok, "service.name not present on the resource")
	assert.Equal(t, "my-relay", serviceName.AsString())
}
