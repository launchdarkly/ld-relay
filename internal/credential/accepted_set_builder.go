package credential

import (
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedSetBuilder accumulates the credentials for an AcceptedSet. Build validates the accumulated
// set (see Build) before returning it.
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
