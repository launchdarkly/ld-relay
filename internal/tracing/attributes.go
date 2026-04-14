package tracing

import "go.opentelemetry.io/otel/attribute"

// Relay-specific span attribute keys.
var (
	SDKKindKey    = attribute.Key("relay.sdk_kind")
	AuthResultKey = attribute.Key("relay.auth.result")
	FlagCountKey  = attribute.Key("relay.flags.count")
	EventsKindKey = attribute.Key("relay.events.kind")
	StoreKeyKey   = attribute.Key("relay.store.key")
)
