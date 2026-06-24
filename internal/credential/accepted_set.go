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

// MalformedCredentialSetError is returned by AcceptedSetBuilder.Build when the set's designated
// anchor SDK key is missing — a violation of the backend invariant that the anchor (sdkKey.value)
// always appears in sdkKeys[]. Validation happens once, at build time; Rotator.Reconcile trusts the
// set it is handed.
//
// Because Build rejects the set before it ever reaches Reconcile, the environment's previous accepted
// set is preserved on a malformed payload. The caller is responsible for the second half of the
// malformed-payload policy: reconnecting the RAC stream with jitter to force a fresh put. RAC is
// one-way push with no NAK channel, so without the reconnect the backend would believe the malformed
// patch was applied and would not send fresh state.
type MalformedCredentialSetError struct {
	// Anchor is the anchor credential that was not found among the set's SDK keys.
	Anchor SDKCredential
}

func (e *MalformedCredentialSetError) Error() string {
	if e.Anchor == nil {
		return "malformed credential set: anchor SDK key is missing"
	}
	return fmt.Sprintf("malformed credential set: anchor SDK key %s is not present in the accepted set",
		e.Anchor.Masked())
}
