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
	FlagCountKey     = attribute.Key("relay.flags.count")
	EventsKindKey    = attribute.Key("relay.events.kind")
	StoreKeyKey      = attribute.Key("relay.store.key")
	PayloadEventsKey = attribute.Key("relay.payload.events")
	PayloadBytesKey  = attribute.Key("relay.payload.bytes")
	ResponseBytesKey = attribute.Key("relay.response.bytes")
)

// Attribute keys identifying a LaunchDarkly environment. Spans and metrics use the same keys so
// that a trace and a metric series for one environment can be joined on either of them.
const (
	EnvNameKey = attribute.Key("environment.name")
	EnvIDKey   = attribute.Key("environment.id")
)

// SanitizeAttributeValue ensures telemetry attribute values are valid. Blank values are replaced
// with a descriptive default, surrounding whitespace is trimmed, and slashes are replaced with
// underscores. This is appropriate for free-form values such as environment names, user agent
// strings, and SDK wrapper names, but not for routes, where slashes are meaningful.
//
// A value that reaches both spans and metrics must go through this in both places, so that each
// signal reports it in the same form. Distinct values can collapse onto the same sanitized value
// (both "a/b" and "a_b" become "a_b"), which merges them wherever they are used as an attribute.
func SanitizeAttributeValue(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "not-provided"
	}
	return strings.ReplaceAll(trimmed, "/", "_")
}
