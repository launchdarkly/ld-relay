package credential

import (
	"fmt"

	"github.com/launchdarkly/ld-relay/v9/config"
)

// AcceptedSet is the full set of credentials an environment accepts after a reconcile: every
// server-side SDK key and mobile key with an optional expiry, the environment ID, and two
// designations.
//
//   - The anchor is the one SDK key that owns the environment's upstream connection.
//   - The primary mobile key is the default used where one mobile key is required, such as event
//     forwarding.
//
// Construct an AcceptedSet with AcceptedSetBuilder. Structural validation of the wire payload happens
// upstream, in BuildAcceptedSet.
type AcceptedSet struct {
	// sdkKeys and mobileKeys store each accepted key once, keyed by value, so duplicates collapse. A
	// nil map is a valid empty set, and only the builder writes.
	sdkKeys          map[config.SDKKey]AcceptedKey
	anchor           config.SDKKey
	mobileKeys       map[config.MobileKey]AcceptedKey
	primaryMobileKey config.MobileKey
	envID            config.EnvironmentID
}

// MalformedCredentialSetError is returned when a credential payload cannot produce a valid
// AcceptedSet. Each constructor below documents one cause.
//
// Validation runs before Reconcile, so the environment keeps its previous accepted set.
//
// A caller reading a one-way push stream with no NAK channel, such as RAC, must also decide whether
// to reconnect: without one the service assumes the payload was applied and sends nothing new. It is
// worth reconnecting only when the whole event was unusable. When the event carried other
// environments that applied, the refused one would be identical on a new connection, and the
// reconnect would interrupt the environments that are working.
type MalformedCredentialSetError struct {
	// msg is the human-readable description set by the constructor.
	msg string
}

func (e *MalformedCredentialSetError) Error() string {
	return e.msg
}

// newMissingAnchorError returns a MalformedCredentialSetError for an absent anchor.
func newMissingAnchorError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: anchor SDK key is missing"}
}

// newNoSDKKeysError returns a MalformedCredentialSetError for a set with no usable SDK key.
func newNoSDKKeysError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: no usable SDK key in sdkKeys[]"}
}

// NewAnchorNotInSetError reports a payload whose designated anchor (sdkKey.value) is defined but
// absent from sdkKeys[]. Credential values are secrets, so no message here includes one.
func NewAnchorNotInSetError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: anchor SDK key is not present in sdkKeys[]"}
}

// NewPrimaryMobileKeyNotInSetError reports a payload whose designated primary mobile key (mobKey) is
// defined but absent from mobileKeys[].
func NewPrimaryMobileKeyNotInSetError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: primary mobile key is not present in mobileKeys[]"}
}

// NewPrimaryMobileKeyMissingError reports a payload with a non-empty mobileKeys[] and no designated
// primary. Accepting it would clear the primary without a repoint, leaving event forwarding on the
// previous key.
func NewPrimaryMobileKeyMissingError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: mobileKeys[] is non-empty but no primary mobile key is designated"}
}

// NewInvalidCredentialCharactersError reports a key-array entry whose value cannot be sent as an HTTP
// header value. Relay presents a credential in an Authorization header, so such a value can never
// authenticate anything upstream. kind is "sdkKeys" or "mobileKeys"; key is the entry's wire
// identifier. No message here includes a credential value.
func NewInvalidCredentialCharactersError(kind, key string) *MalformedCredentialSetError {
	if key == "" {
		return &MalformedCredentialSetError{
			msg: fmt.Sprintf("malformed credential set: %s entry has a value that is not valid in an HTTP header", kind),
		}
	}
	return &MalformedCredentialSetError{
		msg: fmt.Sprintf("malformed credential set: %s entry %q has a value that is not valid in an HTTP header", kind, key),
	}
}

// NewEmptyCredentialError reports a key-array entry whose value field is empty. kind is "sdkKeys" or
// "mobileKeys"; key is the entry's wire identifier, which old-format payloads leave empty.
func NewEmptyCredentialError(kind, key string) *MalformedCredentialSetError {
	if key == "" {
		return &MalformedCredentialSetError{
			msg: fmt.Sprintf("malformed credential set: %s entry has an empty value", kind),
		}
	}
	return &MalformedCredentialSetError{
		msg: fmt.Sprintf("malformed credential set: %s entry %q has an empty value", kind, key),
	}
}
