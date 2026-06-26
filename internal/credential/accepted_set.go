package credential

import (
	"errors"
	"fmt"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedSet is the full set of credentials that an environment should accept after a reconcile.
// It carries every accepted server-side SDK key and mobile key — each with an optional per-key
// expiry — plus the single environment ID and two primary designations:
//
//   - The anchor: the one SDK key that owns the environment's upstream connection. Set with
//     WithPrimarySDKKey.
//   - The primary mobile key: the singular default mobile key (the wire's mobKey), used where one
//     mobile key is required, e.g. event forwarding. Set with WithPrimaryMobileKey.
//
// WithPrimarySDKKey / WithPrimaryMobileKey both add the key to the set and designate it, so adding a
// single key takes one call. Build requires that an anchor was designated. (Structural validation of
// the wire payload — undefined credentials, an anchor absent from the array — happens upstream when
// the payload is parsed into the set; see SDK-2547.)
//
// A key's expiry is taken from its entry in this set; the legacy sdkKey.expiring{} wire slot is not
// consulted when building it.
//
// Construct an AcceptedSet with AcceptedSetBuilder (see accepted_set_builder.go).
type AcceptedSet struct {
	// sdkKeys and mobileKeys store each accepted key once, keyed by value, so duplicates collapse
	// without a containment scan. The map value is the key's expiry: a nil *time.Time means the key
	// is permanent. A nil map is a valid empty set (reads return absent; only the builder writes).
	sdkKeys          map[config.SDKKey]*time.Time
	primarySdkKey    config.SDKKey
	mobileKeys       map[config.MobileKey]*time.Time
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

// errAcceptedSetMissingSDKKey is returned by AcceptedSetBuilder.Build when no SDK key was added. An
// environment must always have at least one SDK key (its anchor), so an empty set indicates a caller
// mistake rather than a benign edge case — surfacing it avoids a silent misconfiguration.
var errAcceptedSetMissingSDKKey = errors.New("accepted credential set must contain at least one SDK key")

// MalformedCredentialSetError is returned when a credential payload cannot produce a valid
// AcceptedSet. This covers two cases:
//
//  1. The anchor SDK key (sdkKey.value) is absent or undefined — a violation of the backend
//     invariant that an anchor is always designated.
//  2. An entry in sdkKeys[] or mobileKeys[] has an empty value — a credential that would be
//     accepted by relay but can never authenticate any SDK.
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

// NewAnchorNotInSetError returns a MalformedCredentialSetError for a payload whose designated anchor
// (sdkKey.value) is defined but not present in the sdkKeys[] array — a structural inconsistency. The
// anchor value is a secret, so it is deliberately not included in the message.
func NewAnchorNotInSetError() *MalformedCredentialSetError {
	return &MalformedCredentialSetError{msg: "malformed credential set: anchor SDK key is not present in sdkKeys[]"}
}

// NewEmptyCredentialError returns a MalformedCredentialSetError for a key-array entry whose
// value field is empty. kind is "sdkKeys" or "mobileKeys"; identifier is the key's identifier
// string (may be empty for old-format payloads that synthesize from the singular fields).
func NewEmptyCredentialError(kind, identifier string) *MalformedCredentialSetError {
	if identifier == "" {
		return &MalformedCredentialSetError{
			msg: fmt.Sprintf("malformed credential set: %s entry has an empty value", kind),
		}
	}
	return &MalformedCredentialSetError{
		msg: fmt.Sprintf("malformed credential set: %s entry %q has an empty value", kind, identifier),
	}
}
