package tracing

import (
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation name used for all relay spans.
const TracerName = "ld-relay"

// Tracer returns the shared relay tracer. When OTLP is disabled the global
// provider is a noop, so every span returned is zero-cost.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }

// Span name constants.
const (
	SpanAuth             = "relay.auth"
	SpanStoreSnapshot    = "relay.store.snapshot"
	SpanStoreGetAll      = "relay.store.get_all"
	SpanStoreGet         = "relay.store.get"
	SpanEvaluateFlags    = "relay.evaluate_flags"
	SpanEventsDispatch   = "relay.events.dispatch"
	SpanSerializePayload = "relay.payload.serialize"
	SpanWriteResponse    = "relay.response.write"
)

// Relay-specific span attribute keys.
const (
	SDKKindKey       = attribute.Key("relay.sdk_kind")
	AuthResultKey    = attribute.Key("relay.auth.result")
	AuthEnvNameKey   = attribute.Key("relay.auth.environment.name")
	AuthEnvIDKey     = attribute.Key("relay.auth.environment.id")
	FlagCountKey     = attribute.Key("relay.flags.count")
	EventsKindKey    = attribute.Key("relay.events.kind")
	StoreKeyKey      = attribute.Key("relay.store.key")
	PayloadEventsKey = attribute.Key("relay.payload.events")
	PayloadBytesKey  = attribute.Key("relay.payload.bytes")
	ResponseBytesKey = attribute.Key("relay.response.bytes")
)

// SanitizeAttributeValue ensures telemetry attribute values are valid. Empty values are replaced
// with a descriptive default, and slashes are replaced with underscores. This is appropriate for
// free-form values such as environment names, user agent strings, and SDK wrapper names, but not
// for routes, where slashes are meaningful.
//
// Span and metric attributes derived from the same source must use this so that the values can be
// correlated across signals.
func SanitizeAttributeValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not-provided"
	}
	return strings.ReplaceAll(v, "/", "_")
}
