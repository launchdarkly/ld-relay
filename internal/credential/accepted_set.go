package credential

import (
	"errors"
	"fmt"
	"slices"
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
// Construct an AcceptedSet with AcceptedSetBuilder.
type AcceptedSet struct {
	sdkKeys          []acceptedSDKKey
	primarySdkKey    config.SDKKey
	mobileKeys       []acceptedMobileKey
	primaryMobileKey config.MobileKey
	envID            config.EnvironmentID
}

type acceptedSDKKey struct {
	key    config.SDKKey
	expiry *time.Time // nil = permanent
}

type acceptedMobileKey struct {
	key    config.MobileKey
	expiry *time.Time // nil = permanent
}

// hasSDKKey reports whether key is one of the set's accepted SDK keys.
func (s AcceptedSet) hasSDKKey(key config.SDKKey) bool {
	return slices.ContainsFunc(s.sdkKeys, func(k acceptedSDKKey) bool {
		return k.key == key
	})
}

// hasMobileKey reports whether key is one of the set's accepted mobile keys.
func (s AcceptedSet) hasMobileKey(key config.MobileKey) bool {
	return slices.ContainsFunc(s.mobileKeys, func(k acceptedMobileKey) bool {
		return k.key == key
	})
}

// errAcceptedSetMissingSDKKey is returned by AcceptedSetBuilder.Build when no SDK key was added. An
// environment must always have at least one SDK key (its anchor), so an empty set indicates a caller
// mistake rather than a benign edge case — surfacing it avoids a silent misconfiguration.
var errAcceptedSetMissingSDKKey = errors.New("accepted credential set must contain at least one SDK key")

// AcceptedSetBuilder accumulates the credentials for an AcceptedSet. Build validates that the
// accumulated set has at least one SDK key before returning it.
type AcceptedSetBuilder struct {
	set AcceptedSet
}

// NewAcceptedSetBuilder returns an empty AcceptedSetBuilder.
func NewAcceptedSetBuilder() *AcceptedSetBuilder {
	return &AcceptedSetBuilder{}
}

// WithSDKKey adds a permanent (non-expiring) SDK key. It is a no-op if the key is undefined or
// already present.
func (b *AcceptedSetBuilder) WithSDKKey(key config.SDKKey) *AcceptedSetBuilder {
	b.addSDKKey(key, nil)
	return b
}

// WithExpiringSDKKey adds an SDK key that should be accepted until the given expiry. It is a no-op
// if the key is undefined or already present.
func (b *AcceptedSetBuilder) WithExpiringSDKKey(key config.SDKKey, expiry time.Time) *AcceptedSetBuilder {
	b.addSDKKey(key, &expiry)
	return b
}

// WithPrimarySDKKey adds key (if not already present) and designates it as the anchor — the SDK key
// that owns the environment's upstream connection. It is a no-op if the key is undefined.
func (b *AcceptedSetBuilder) WithPrimarySDKKey(key config.SDKKey) *AcceptedSetBuilder {
	if key.Defined() {
		b.addSDKKey(key, nil)
		b.set.primarySdkKey = key
	}
	return b
}

// addSDKKey appends the key with the given expiry (nil = permanent), skipping undefined keys and
// keys already in the set.
func (b *AcceptedSetBuilder) addSDKKey(key config.SDKKey, expiry *time.Time) {
	if !key.Defined() || b.set.hasSDKKey(key) {
		return
	}
	b.set.sdkKeys = append(b.set.sdkKeys, acceptedSDKKey{key: key, expiry: expiry})
}

// WithMobileKey adds a permanent (non-expiring) mobile key. It is a no-op if the key is undefined or
// already present.
func (b *AcceptedSetBuilder) WithMobileKey(key config.MobileKey) *AcceptedSetBuilder {
	b.addMobileKey(key, nil)
	return b
}

// WithExpiringMobileKey adds a mobile key that should be accepted until the given expiry. It is a
// no-op if the key is undefined or already present.
func (b *AcceptedSetBuilder) WithExpiringMobileKey(key config.MobileKey, expiry time.Time) *AcceptedSetBuilder {
	b.addMobileKey(key, &expiry)
	return b
}

// WithPrimaryMobileKey adds key (if not already present) and designates it as the primary mobile
// key — the singular default (the wire's mobKey) used where one mobile key is required, e.g. event
// forwarding. It is a no-op if the key is undefined.
func (b *AcceptedSetBuilder) WithPrimaryMobileKey(key config.MobileKey) *AcceptedSetBuilder {
	if key.Defined() {
		b.addMobileKey(key, nil)
		b.set.primaryMobileKey = key
	}
	return b
}

// addMobileKey appends the key with the given expiry (nil = permanent), skipping undefined keys and
// keys already in the set.
func (b *AcceptedSetBuilder) addMobileKey(key config.MobileKey, expiry *time.Time) {
	if !key.Defined() || b.set.hasMobileKey(key) {
		return
	}
	b.set.mobileKeys = append(b.set.mobileKeys, acceptedMobileKey{key: key, expiry: expiry})
}

// WithEnvironmentID sets the environment ID. It is a no-op if the ID is undefined.
func (b *AcceptedSetBuilder) WithEnvironmentID(id config.EnvironmentID) *AcceptedSetBuilder {
	if id.Defined() {
		b.set.envID = id
	}
	return b
}

// Build validates and returns the accumulated AcceptedSet. It returns errAcceptedSetMissingSDKKey if
// no SDK key was added, or a *MalformedCredentialSetError if no anchor was designated (via
// WithPrimarySDKKey). Because WithPrimarySDKKey also adds the key, a designated anchor is always
// among the accepted SDK keys.
func (b *AcceptedSetBuilder) Build() (AcceptedSet, error) {
	if len(b.set.sdkKeys) == 0 {
		return AcceptedSet{}, errAcceptedSetMissingSDKKey
	}
	if !b.set.primarySdkKey.Defined() {
		return AcceptedSet{}, &MalformedCredentialSetError{Anchor: nil}
	}
	return b.set, nil
}

// MalformedCredentialSetError is returned by AcceptedSetBuilder.Build and Rotator.Reconcile when the
// set's designated anchor SDK key is missing or is not present among the set's SDK keys — a
// violation of the backend invariant that the anchor (sdkKey.value) always appears in sdkKeys[].
//
// When Reconcile returns this error it has made no changes, so the environment's previous accepted
// set is preserved. The caller is responsible for the second half of the malformed-payload policy:
// reconnecting the RAC stream with jitter to force a fresh put. RAC is one-way push with no NAK
// channel, so without the reconnect the backend would believe the malformed patch was applied and
// would not send fresh state.
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
