package credential

import (
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedSet is the full set of credentials that an environment should accept after a reconcile.
// It carries every accepted server-side SDK key and mobile key — each with an optional per-key
// expiry — plus the single environment ID and two primary designations:
//
//   - The anchor: the one SDK key that owns the environment's upstream connection. Set with
//     WithAnchor.
//   - The primary mobile key: the singular default mobile key (the wire's mobKey), used where one
//     mobile key is required, e.g. event forwarding. Set with WithPrimaryMobileKey.
//
// WithAnchor / WithPrimaryMobileKey both add the key to the set and designate it, so adding a
// single key takes one call. Build requires that an anchor was designated. (Structural validation of
// the wire payload — undefined credentials, a designated key absent from its array — happens upstream
// when the payload is parsed into the set.)
//
// A key's expiry is taken from its entry in this set; the legacy sdkKey.expiring{} wire slot is not
// consulted when building it.
//
// Construct an AcceptedSet with AcceptedSetBuilder (see accepted_set_builder.go).
type AcceptedSet struct {
	// sdkKeys and mobileKeys store each accepted key once, keyed by value (the secret), so duplicates
	// collapse without a containment scan. The map value carries the key's metadata (see
	// AcceptedKey). A nil map is a valid empty set (reads return absent; only the builder writes).
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
// AcceptedSet. This covers:
//
//  1. The anchor SDK key (sdkKey.value) is absent or undefined, or defined but not present in
//     sdkKeys[] — a violation of the invariant that the designated anchor is one of the accepted keys.
//  2. The primary mobile key (mobKey) is defined but not present in mobileKeys[] — the mobile-key
//     analogue of the anchor invariant.
//  3. mobileKeys[] is non-empty but no primary mobile key (mobKey) is designated — accepting it would
//     clear the environment's primary mobile key on reconcile with no repoint, so event forwarding
//     would keep using the previous (possibly revoked) primary. (No mobile keys at all is valid.)
//  4. An entry in sdkKeys[] or mobileKeys[] has an empty value — a credential that would be
//     accepted by relay but can never authenticate any SDK.
//  5. No SDK key survived at all, so the environment would have nothing to authenticate with. A
//     payload reaches this by combining an undefined anchor with an sdkKeys[] array that is either
//     empty or entirely made up of keys relay excludes, such as keys scoped to a view.
//
// Validation happens before Reconcile is called; Rotator.Reconcile trusts the set it is handed.
// Because the error is raised before any state mutation, the environment's previous accepted set is
// preserved automatically. The caller is responsible for the second half of the malformed-payload
// policy: reconnecting the RAC stream with jitter to force a fresh put. RAC is one-way push with no
// NAK channel, so without the reconnect the backend would believe the malformed patch was applied
// and would not send fresh state.
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

// newNoSDKKeysError returns a MalformedCredentialSetError for a set that ended up with no SDK key at
// all. The message describes the payload rather than the builder, because that is what an operator
// reading the log can act on.
func newNoSDKKeysError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: no usable SDK key in sdkKeys[]"}
}

// NewAnchorNotInSetError returns a MalformedCredentialSetError for a payload whose designated anchor
// (sdkKey.value) is defined but not present in the sdkKeys[] array — a structural inconsistency. The
// anchor value is a secret, so it is deliberately not included in the message.
func NewAnchorNotInSetError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: anchor SDK key is not present in sdkKeys[]"}
}

// NewPrimaryMobileKeyNotInSetError returns a MalformedCredentialSetError for a payload whose
// designated primary mobile key (mobKey) is defined but not present in the mobileKeys[] array — the
// mobile-key analogue of NewAnchorNotInSetError. The key value is a secret, so it is deliberately not
// included in the message.
func NewPrimaryMobileKeyNotInSetError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: primary mobile key is not present in mobileKeys[]"}
}

// NewPrimaryMobileKeyMissingError returns a MalformedCredentialSetError for a payload that carries a
// non-empty mobileKeys[] array but leaves the primary mobile key (mobKey) undefined — no default is
// designated. Accepting it would clear the environment's primary mobile key on reconcile with no
// repoint, so event forwarding would keep using the previous (possibly revoked) primary. There are no
// secrets to omit from the message.
func NewPrimaryMobileKeyMissingError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: mobileKeys[] is non-empty but no primary mobile key is designated"}
}

// NewEmptyCredentialError returns a MalformedCredentialSetError for a key-array entry whose
// value field is empty. kind is "sdkKeys" or "mobileKeys"; key is the entry's wire "key" identifier
// (may be empty for old-format payloads that synthesize from the singular fields).
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
