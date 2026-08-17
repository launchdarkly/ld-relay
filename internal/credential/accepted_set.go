package credential

import (
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/config"
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

// hasSDKKey reports whether key is one of the set's accepted SDK keys.
func (s AcceptedSet) hasSDKKey(key config.SDKKey) bool {
	_, ok := s.sdkKeys[key]
	return ok
}

// hasMobileKey reports whether key is one of the set's accepted mobile keys.
func (s AcceptedSet) hasMobileKey(key config.MobileKey) bool {
	_, ok := s.mobileKeys[key]
	return ok
}

// MalformedCredentialSetError is returned when a credential payload cannot produce a valid
// AcceptedSet. Each constructor below documents one cause.
//
// Validation runs before Reconcile, so the environment keeps its previous accepted set. The caller
// must also reconnect the RAC stream with jitter: RAC is one-way push with no NAK channel, so
// without a reconnect the backend assumes the patch was applied and sends nothing new.
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
