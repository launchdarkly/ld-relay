package tracing

import (
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
