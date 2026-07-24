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
	// SpanConcurrencyWait covers a request's time inside Limiter.Acquire: queueing
	// for a token when the limiter is saturated. It ends when the request is
	// admitted or rejected, before any handler work runs.
	SpanConcurrencyWait = "relay.concurrency.wait"
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
	// PayloadCacheHitKey reports whether a serialize span was satisfied from the
	// per-environment payload cache instead of encoding the snapshot.
	PayloadCacheHitKey = attribute.Key("relay.payload.cache_hit")
	// PayloadStreamedKey reports that the payload was encoded directly to the network,
	// so the serialize span also covers the response write.
	PayloadStreamedKey = attribute.Key("relay.payload.streamed")
	// ResponseEncodingKey is the content encoding of the response body ("gzip" or
	// "identity"), recorded when the handler chooses between pre-encoded variants.
	ResponseEncodingKey = attribute.Key("relay.response.encoding")

	// Attributes of the relay.concurrency.wait span. Held and waiting are sampled on
	// entry, before this request acquires, so they describe the congestion the
	// request encountered.
	ConcurrencyLimiterKey  = attribute.Key("relay.concurrency.limiter")
	ConcurrencyAdmittedKey = attribute.Key("relay.concurrency.admitted")
	ConcurrencyHeldKey     = attribute.Key("relay.concurrency.held")
	ConcurrencyWaitingKey  = attribute.Key("relay.concurrency.waiting")
	ConcurrencyMaxKey      = attribute.Key("relay.concurrency.max_concurrent")
)
