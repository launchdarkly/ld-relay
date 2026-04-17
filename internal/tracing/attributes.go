package tracing

import "go.opentelemetry.io/otel/attribute"

// Relay-specific span attribute keys.
var (
	SDKKindKey    = attribute.Key("relay.sdk_kind")    //nolint:gochecknoglobals
	AuthResultKey = attribute.Key("relay.auth.result") //nolint:gochecknoglobals
	FlagCountKey  = attribute.Key("relay.flags.count") //nolint:gochecknoglobals
	EventsKindKey = attribute.Key("relay.events.kind") //nolint:gochecknoglobals
	StoreKeyKey   = attribute.Key("relay.store.key")   //nolint:gochecknoglobals
)
