package wire

// ProtocolVersion is the MegaStream protocol version this relay speaks.
const ProtocolVersion = 1

// Message type identifiers. The handshake messages spell the field out as "type"; every
// envelope after the handshake uses the compact "t". See handshake.go for that split.
const (
	TypeHello              = "hello"
	TypeWelcome            = "welcome"
	TypeServerIntent       = "server-intent"
	TypePutObject          = "put-object"
	TypeDeleteObject       = "delete-object"
	TypePayloadTransferred = "payload-transferred"
	TypeRef                = "ref"
	TypeRefDelete          = "ref-delete"
	TypeRefBatch           = "ref-batch"
	TypeError              = "error"
	TypeHeartbeat          = "heartbeat"
	TypeHeartbeatAck       = "heartbeat-ack"
	TypeGoodbye            = "goodbye"
	TypeScopeAdd           = "scope-add"
	TypeScopeRemove        = "scope-remove"
)

// Capabilities the relay can negotiate. The server replies with the intersection of both
// sides' lists. MessagePack is implicitly required, so the relay may omit it when offering.
const (
	CapabilityMsgpack   = "msgpack"
	CapabilityBatchRefs = "batch-refs"
	CapabilityZstd      = "zstd"
)

// Transfer intents a server-intent can carry.
const (
	// IntentTransferFull means a full hydration follows. For a scope that already holds
	// membership this is a resync: rebuild from the delivery and keep nothing it does not
	// re-assert.
	IntentTransferFull = "xfer-full"
	// IntentTransferChanges means the scope resumed and only changes since the presented
	// state follow.
	IntentTransferChanges = "xfer-changes"
	// IntentNone means the presented state is already current. The delivery is the cursor
	// alone.
	IntentNone = "none"
)

// Error codes registered in v1. The set is open: new codes arrive through capability
// negotiation, so an unrecognized code is informational rather than an error.
const (
	ErrorInvalidKey              = "invalid-key"
	ErrorScopeRevoked            = "scope-revoked"
	ErrorVerificationUnavailable = "verification-unavailable"
	ErrorScopeLimit              = "scope-limit"
	ErrorScopeRateLimit          = "scope-rate-limit"
	ErrorBadMessage              = "bad-message"
	ErrorInternal                = "internal"
)

// Goodbye codes registered in v1. The set is open; an unrecognized code gets the ordinary
// reconnect rules.
const (
	GoodbyeDraining               = "draining"
	GoodbyeTooSlow                = "too-slow"
	GoodbyeUnresponsive           = "unresponsive"
	GoodbyeHandshakeTimeout       = "handshake-timeout"
	GoodbyeInvalidRoot            = "invalid-root"
	GoodbyeRootNotEnvironmentWide = "root-not-environment-wide"
	GoodbyeMixedEnvironment       = "mixed-environment"
	GoodbyeRootRevoked            = "root-revoked"
	GoodbyePayloadReset           = "payload-reset"
	GoodbyeEarlyReconnect         = "early-reconnect"
	GoodbyeProtocolViolation      = "protocol-violation"
	GoodbyeTransient              = "transient"
	GoodbyeInternal               = "internal"
)

// Hello is the first message the relay sends. It names the root credential that authorizes
// the session and registers every scope the relay intends to serve. A scope entry may carry
// the state selector recorded for it, which resumes that scope instead of re-hydrating it.
//
// This message is JSON in a text frame.
type Hello struct {
	Type         string       `json:"type"`
	Version      int          `json:"version"`
	RelayVersion string       `json:"relayVersion,omitempty"`
	Capabilities []string     `json:"capabilities"`
	Root         string       `json:"root"`
	Scopes       []HelloScope `json:"scopes"`
	// InstanceID is stable for this relay instance and survives reconnects. It lets the
	// server tell this instance's early reconnect from a sibling's ordinary one.
	InstanceID string `json:"instanceId,omitempty"`
}

// HelloScope is one scope registration in a Hello. State is empty when the relay has no
// recorded selector for the scope, or when it chooses full re-hydration.
type HelloScope struct {
	Key   string `json:"key"`
	State string `json:"state,omitempty"`
}

// Welcome accepts the handshake. Capabilities is the negotiated set. Base is the payload ID
// that addresses base-scope content, and it is fixed for the session's life.
//
// This message is JSON in a text frame.
type Welcome struct {
	Type                string   `json:"type"`
	Version             int      `json:"version"`
	Capabilities        []string `json:"capabilities"`
	HeartbeatIntervalMs int      `json:"heartbeatIntervalMs"`
	Base                string   `json:"base"`
	// MaxScopes is the number of scopes this session may hold. Zero means the server did
	// not advertise a bound; the schema forbids a real bound of zero.
	MaxScopes int `json:"maxScopes,omitempty"`
}

// ServerIntent opens a scope's delivery.
//
// Unfiltered declares that the scope has implicit full membership and receives no per-object
// refs. It is a pointer because the field is required and its absence must be an error
// rather than a false: an empty view receives no refs either, so inferring "filtered" from a
// missing declaration would silently reduce an environment-wide scope to serving nothing.
// Use IsUnfiltered to read it, which reports whether the server declared it at all.
type ServerIntent struct {
	T          string `msgpack:"t"`
	Scope      string `msgpack:"scope"`
	IntentCode string `msgpack:"intentCode"`
	Reason     string `msgpack:"reason,omitempty"`
	Unfiltered *bool  `msgpack:"unfiltered"`
}

// IsUnfiltered reports the scope's membership declaration. The second result is false when
// the server omitted the field, which the schema forbids and a receiver must not paper over.
func (i ServerIntent) IsUnfiltered() (bool, bool) {
	if i.Unfiltered == nil {
		return false, false
	}
	return *i.Unfiltered, true
}

// PutObject carries base-scope object content. Each object is sent once however many scopes
// reference it. Version is the base payload version at which the change applies, not the
// object's own version, and a membership-driven re-entry may repeat it.
//
// Object holds the object's JSON representation as opaque bytes. The relay stores and
// forwards those bytes without re-encoding them.
type PutObject struct {
	T       string         `msgpack:"t"`
	Base    string         `msgpack:"base"`
	Kind    string         `msgpack:"kind"`
	Key     string         `msgpack:"key"`
	Version PayloadVersion `msgpack:"version"`
	Object  []byte         `msgpack:"object"`
}

// DeleteObject evicts an object from the base scope. It leaves every scope's membership
// untouched: content and membership are separate planes.
type DeleteObject struct {
	T       string         `msgpack:"t"`
	Base    string         `msgpack:"base"`
	Kind    string         `msgpack:"kind"`
	Key     string         `msgpack:"key"`
	Version PayloadVersion `msgpack:"version"`
}

// PayloadTransferred is a scope's version cursor. The first one for a scope signals that the
// scope is hydrated. State is opaque: the relay forwards it downstream verbatim and presents
// it back at the next handshake to resume.
type PayloadTransferred struct {
	T       string         `msgpack:"t"`
	Scope   string         `msgpack:"scope"`
	State   string         `msgpack:"state"`
	Version PayloadVersion `msgpack:"version"`
}

// Ref asserts that an object belongs to a scope's membership. A ref may arrive before its
// object's content, which is pending membership rather than an error.
type Ref struct {
	T     string `msgpack:"t"`
	Scope string `msgpack:"scope"`
	Kind  string `msgpack:"kind"`
	Key   string `msgpack:"key"`
}

// RefDelete retracts an object from a scope's membership. The object may stay in the base
// scope for other scopes.
type RefDelete struct {
	T     string `msgpack:"t"`
	Scope string `msgpack:"scope"`
	Kind  string `msgpack:"kind"`
	Key   string `msgpack:"key"`
}

// ObjectRef identifies one object inside a RefBatch.
type ObjectRef struct {
	Kind string `msgpack:"kind"`
	Key  string `msgpack:"key"`
}

// RefBatch carries several membership changes for one scope. The server sends it only when
// both sides negotiated the batch-refs capability.
type RefBatch struct {
	T       string      `msgpack:"t"`
	Scope   string      `msgpack:"scope"`
	Refs    []ObjectRef `msgpack:"refs,omitempty"`
	Deletes []ObjectRef `msgpack:"deletes,omitempty"`
}

// ErrorMessage reports a failure in either direction. A scope-tagged error is contained to
// that scope and never terminates the connection. An untagged error is advisory: only
// Goodbye signals that the session is ending.
//
// The name avoids Error because this type is a message rather than a Go error. Its codes
// mean different things to the registry, and most of them are not failures of the session.
type ErrorMessage struct {
	T       string `msgpack:"t" json:"t"`
	Scope   string `msgpack:"scope,omitempty" json:"scope,omitempty"`
	Code    string `msgpack:"code" json:"code"`
	Message string `msgpack:"message" json:"message"`
}

// Heartbeat is the server's liveness probe, sent when the stream is otherwise idle.
type Heartbeat struct {
	T string `msgpack:"t"`
}

// HeartbeatAck answers a Heartbeat.
type HeartbeatAck struct {
	T string `msgpack:"t"`
}

// Goodbye closes the session. BackoffMs, when present, is a minimum reconnect delay the
// relay honors.
type Goodbye struct {
	T         string `msgpack:"t" json:"t"`
	Reason    string `msgpack:"reason" json:"reason"`
	BackoffMs int    `msgpack:"backoffMs,omitempty" json:"backoffMs,omitempty"`
	Code      string `msgpack:"code,omitempty" json:"code,omitempty"`
}

// ScopeAdd registers one more credential on a live session. It is idempotent: the server
// treats a duplicate as a no-op.
type ScopeAdd struct {
	T   string `msgpack:"t"`
	Key string `msgpack:"key"`
}

// ScopeRemove deregisters a credential.
type ScopeRemove struct {
	T     string `msgpack:"t"`
	Scope string `msgpack:"scope"`
}

// Unknown is an envelope whose type this relay does not recognize. The protocol requires
// ignoring one rather than failing the connection, so the codec surfaces it as a value the
// caller can log instead of returning an error.
type Unknown struct {
	T string `msgpack:"t"`
}

// HasCapability reports whether a negotiated capability set contains name.
func HasCapability(capabilities []string, name string) bool {
	for _, c := range capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// IsConfigurationError reports whether a goodbye code describes a relay configuration
// problem. None of these resolves without an operator editing the configuration, so the
// session parks on a long retry schedule instead of the ordinary one.
func IsConfigurationError(code string) bool {
	switch code {
	case GoodbyeInvalidRoot, GoodbyeRootNotEnvironmentWide, GoodbyeMixedEnvironment:
		return true
	default:
		return false
	}
}
