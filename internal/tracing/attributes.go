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

	// SingleflightSharedKey reports, on the request span of a polling request or an SSE
	// replay, whether the payload build was shared with concurrent requests through a flight
	// group. When it is true and the request's trace shows no sign of the build itself,
	// another request's trace carries it.
	SingleflightSharedKey = attribute.Key("relay.singleflight.shared")

	// SingleflightWaitMSKey reports, on the request span of a request that received its
	// payload from a flight another request was already executing, how many milliseconds it
	// spent waiting for that flight. It is absent from the request that executed the build:
	// that request did not wait.
	SingleflightWaitMSKey = attribute.Key("relay.singleflight.wait_ms")
)
