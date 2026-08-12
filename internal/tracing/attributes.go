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
	SpanEvaluateFlags    = "relay.flags.evaluate"
	SpanEventsDispatch   = "relay.events.dispatch"
	SpanSerializePayload = "relay.payload.serialize"
	SpanWriteResponse    = "relay.response.write"
	SpanSingleflightWait = "relay.singleflight.wait"
)

// Relay-specific span attribute keys.
const (
	// SDKKindKey reports the category of SDK that made the request. It describes the caller rather
	// than Relay itself, so it sits beside the other LaunchDarkly attributes rather than under
	// launchdarkly.relay.
	SDKKindKey    = attribute.Key("launchdarkly.sdk.kind")
	AuthResultKey = attribute.Key("launchdarkly.relay.auth.result")
	EventsKindKey = attribute.Key("launchdarkly.relay.events.kind")
	StoreKeyKey   = attribute.Key("launchdarkly.relay.store.key")

	// FlagCountKey, PayloadEventCountKey and PayloadSizeKey report how much a payload contained.
	// Quantities follow one shape: a count of something ends in .count, and a size in bytes ends in
	// .size, with the unit left out of the name.
	FlagCountKey         = attribute.Key("launchdarkly.relay.flag.count")
	PayloadEventCountKey = attribute.Key("launchdarkly.relay.payload.event.count")
	PayloadSizeKey       = attribute.Key("launchdarkly.relay.payload.size")

	// SingleflightSharedKey reports, on the request span of a polling request or an SSE
	// replay, whether the payload build was shared with concurrent requests through a flight
	// group. When it is true and the request's trace shows no sign of the build itself,
	// another request's trace carries it.
	SingleflightSharedKey = attribute.Key("launchdarkly.relay.singleflight.shared")

	// SingleflightWaitDurationKey reports, on the request span of a request that received its
	// payload from a flight another request was already executing, how long it spent waiting for
	// that flight. The value is in seconds, OTel's base unit for a duration, so the unit stays out
	// of the name. It is absent from the request that executed the build: that request did not
	// wait. The same window is also visible in the trace timeline as a SpanSingleflightWait child
	// span.
	SingleflightWaitDurationKey = attribute.Key("launchdarkly.relay.singleflight.wait.duration")
)
