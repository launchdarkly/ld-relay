package credential

import (
	"errors"
	"fmt"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedSet is the full set of credentials that an environment should accept after a reconcile.
// It carries every accepted server-side SDK key and mobile key — each with an optional per-key
// expiry — plus the single environment ID. The anchor (the one SDK key that owns the environment's
// upstream connection) is supplied separately to Rotator.Reconcile.
//
// A key's expiry is taken from its entry in this set; the legacy sdkKey.expiring{} wire slot is not
// consulted when building it.
//
// Construct an AcceptedSet with AcceptedSetBuilder.
type AcceptedSet struct {
	sdkKeys    []acceptedSDKKey
	mobileKeys []acceptedMobileKey
	envID      config.EnvironmentID
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
	for _, k := range s.sdkKeys {
		if k.key == key {
			return true
		}
	}
	return false
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

// WithSDKKey adds a permanent (non-expiring) SDK key. It is a no-op if the key is undefined.
func (b *AcceptedSetBuilder) WithSDKKey(key config.SDKKey) *AcceptedSetBuilder {
	if key.Defined() {
		b.set.sdkKeys = append(b.set.sdkKeys, acceptedSDKKey{key: key})
	}
	return b
}

// WithExpiringSDKKey adds an SDK key that should be accepted until the given expiry. It is a no-op
// if the key is undefined.
func (b *AcceptedSetBuilder) WithExpiringSDKKey(key config.SDKKey, expiry time.Time) *AcceptedSetBuilder {
	if key.Defined() {
		b.set.sdkKeys = append(b.set.sdkKeys, acceptedSDKKey{key: key, expiry: &expiry})
	}
	return b
}

// WithMobileKey adds a permanent (non-expiring) mobile key. It is a no-op if the key is undefined.
func (b *AcceptedSetBuilder) WithMobileKey(key config.MobileKey) *AcceptedSetBuilder {
	if key.Defined() {
		b.set.mobileKeys = append(b.set.mobileKeys, acceptedMobileKey{key: key})
	}
	return b
}

// WithExpiringMobileKey adds a mobile key that should be accepted until the given expiry. It is a
// no-op if the key is undefined.
func (b *AcceptedSetBuilder) WithExpiringMobileKey(key config.MobileKey, expiry time.Time) *AcceptedSetBuilder {
	if key.Defined() {
		b.set.mobileKeys = append(b.set.mobileKeys, acceptedMobileKey{key: key, expiry: &expiry})
	}
	return b
}

// WithEnvironmentID sets the environment ID. It is a no-op if the ID is undefined.
func (b *AcceptedSetBuilder) WithEnvironmentID(id config.EnvironmentID) *AcceptedSetBuilder {
	if id.Defined() {
		b.set.envID = id
	}
	return b
}

// Build validates and returns the accumulated AcceptedSet. It returns an error if no SDK key was
// added.
func (b *AcceptedSetBuilder) Build() (AcceptedSet, error) {
	if len(b.set.sdkKeys) == 0 {
		return AcceptedSet{}, errAcceptedSetMissingSDKKey
	}
	return b.set, nil
}

// MalformedCredentialSetError is returned by Rotator.Reconcile when the supplied anchor SDK key is
// not present among the accepted set's SDK keys — a violation of the backend invariant that the
// anchor (sdkKey.value) always appears in sdkKeys[].
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
