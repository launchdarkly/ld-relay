package tracing

import (
	"log/slog"
	"testing"
	"unicode/utf8"

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

// An attribute whose key merely contains "service.instance.id" is a different attribute, so the
// generated process identity must still be reported. Relay would otherwise report no process
// identity at all.
func TestResourceStillReportsAnIdentityAlongsideSimilarlyNamedAttributes(t *testing.T) {
	for _, attrs := range []string{
		"service.instance.id.source=kubernetes",
		"custom.service.instance.id=pod-7",
		"deployment.note=set service.instance.id here",
	} {
		t.Run(attrs, func(t *testing.T) {
			t.Setenv("OTEL_SERVICE_NAME", "")
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", attrs)

			res := NewResource(slog.Default())
			require.NotNil(t, res)

			instanceID, ok := res.Set().Value(attribute.Key("service.instance.id"))
			require.True(t, ok, "service.instance.id not present on the resource")
			assert.Equal(t, RelayID(), instanceID.AsString())
		})
	}
}

// Either environment variable can carry the service name, and OTEL_SERVICE_NAME wins between them.
// Both win over the built-in default.
func TestResourceUsesTheConfiguredServiceName(t *testing.T) {
	for _, params := range []struct{ name, serviceName, attrs, want string }{
		{"from OTEL_SERVICE_NAME", "my-relay", "", "my-relay"},
		{"from OTEL_RESOURCE_ATTRIBUTES", "", "service.name=my-relay", "my-relay"},
		{"OTEL_SERVICE_NAME wins", "from-env", "service.name=from-attrs", "from-env"},
	} {
		t.Run(params.name, func(t *testing.T) {
			t.Setenv("OTEL_SERVICE_NAME", params.serviceName)
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", params.attrs)

			res := NewResource(slog.Default())
			require.NotNil(t, res)

			serviceName, ok := res.Set().Value(attribute.Key("service.name"))
			require.True(t, ok, "service.name not present on the resource")
			assert.Equal(t, params.want, serviceName.AsString())
		})
	}
}

// A malformed OTEL_RESOURCE_ATTRIBUTES entry must not cost Relay its identity. resource.New reports a
// partial-resource error but still returns everything it merged, so the resource stays usable. A stray
// comma is the realistic way to reach this -- templated configuration produces one whenever a
// conditional attribute renders empty.
func TestResourceSurvivesAMalformedResourceAttribute(t *testing.T) {
	for _, attrs := range []string{
		"no-equals-sign",
		"service.namespace=relay,",
		",service.namespace=relay",
		"service.namespace=relay,,deployment.environment=production",
		",",
	} {
		t.Run(attrs, func(t *testing.T) {
			t.Setenv("OTEL_SERVICE_NAME", "")
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", attrs)

			res := NewResource(slog.Default())
			require.NotNil(t, res)

			serviceName, ok := res.Set().Value(attribute.Key("service.name"))
			require.True(t, ok, "service.name not present on the resource")
			assert.Equal(t, "ld-relay", serviceName.AsString())

			instanceID, ok := res.Set().Value(attribute.Key("service.instance.id"))
			require.True(t, ok, "service.instance.id not present on the resource")
			assert.Equal(t, RelayID(), instanceID.AsString())
		})
	}
}

// Every attribute that did parse is kept, alongside Relay's defaults.
func TestResourceKeepsTheValidAttributesBesideAMalformedOne(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.namespace=relay,oops,deployment.environment=production")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	namespace, ok := res.Set().Value(attribute.Key("service.namespace"))
	require.True(t, ok, "service.namespace was discarded")
	assert.Equal(t, "relay", namespace.AsString())

	environment, ok := res.Set().Value(attribute.Key("deployment.environment"))
	require.True(t, ok, "deployment.environment was discarded")
	assert.Equal(t, "production", environment.AsString())
}

// An operator's own service.instance.id survives a malformed entry elsewhere in the variable. Falling
// back to the default resource would have discarded the value they explicitly supplied.
func TestResourceKeepsAnOperatorInstanceIDBesideAMalformedEntry(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=operator-id,oops")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	instanceID, ok := res.Set().Value(attribute.Key("service.instance.id"))
	require.True(t, ok, "service.instance.id not present on the resource")
	assert.Equal(t, "operator-id", instanceID.AsString())
}

// OTEL_RESOURCE_ATTRIBUTES values are percent-decoded, so an operator can put a byte outside UTF-8
// into one without meaning to. The resource travels with every export batch, so one invalid byte would
// stop all telemetry for the life of the process.
func TestResourceAttributesAreValidUTF8(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.note=%ff%fe,service.namespace=relay")

	res := NewResource(slog.Default())
	require.NotNil(t, res)

	note, ok := res.Set().Value(attribute.Key("deployment.note"))
	require.True(t, ok, "deployment.note not present on the resource")
	assert.True(t, utf8.ValidString(note.AsString()),
		"an invalid byte reached a resource attribute: %q", note.AsString())
	assert.Empty(t, note.AsString())

	for _, kv := range res.Attributes() {
		if kv.Value.Type() == attribute.STRING {
			assert.True(t, utf8.ValidString(kv.Value.AsString()), "attribute %s is not valid UTF-8", kv.Key)
		}
	}

	namespace, ok := res.Set().Value(attribute.Key("service.namespace"))
	require.True(t, ok, "the valid attribute alongside it was discarded")
	assert.Equal(t, "relay", namespace.AsString())
}
