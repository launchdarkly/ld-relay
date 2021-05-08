package basictypes

// StreamKind is an enum-like type of the supported kinds of SDK streaming endpoint. This is different
// from basictypes.SDKKind because there are more kinds of streams than there are kinds of SDKs (server-side
// SDKs have two streams).
type StreamKind string

const (
	// ServerSideStream represents the server-side SDK "/all" endpoint, which is used by all current
	// server-side SDK versions.
	ServerSideStream StreamKind = "server"

	// ServerSideFlagsOnlyStream represents the server-side SDK "/flags" endpoint, which is used only
	// by old SDKs that do not support segments.
	ServerSideFlagsOnlyStream StreamKind = "server-flags"

	// MobilePingStream represents the mobile streaming endpoints, which will generate only "ping" events.
	MobilePingStream StreamKind = "mobile-ping"

	// JSClientPingStream represents the JS client-side streaming endpoints, which will generate only
	// "ping" events. This is identical to MobilePingStream except that it only handles requests
	// authenticated with an environment ID.
	JSClientPingStream StreamKind = "js-ping"
)
