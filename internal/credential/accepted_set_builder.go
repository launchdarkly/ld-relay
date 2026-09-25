package credential

import (
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
)

// AcceptedSetBuilder accumulates the credentials for an AcceptedSet. Build validates the accumulated
// set (see Build) before returning it.
type AcceptedSetBuilder struct {
	set AcceptedSet
}

// NewAcceptedSetBuilder returns an empty AcceptedSetBuilder.
func NewAcceptedSetBuilder() *AcceptedSetBuilder {
	return &AcceptedSetBuilder{
		set: AcceptedSet{
			sdkKeys:    make(map[config.SDKKey]AcceptedKey),
			mobileKeys: make(map[config.MobileKey]AcceptedKey),
		},
	}
}

// SDKKeyParams describes one accepted server-side SDK key for the builder: the credential value plus
// the optional wire "key" identifier (nil when absent) and optional expiry (nil = permanent).
type SDKKeyParams struct {
	Value  config.SDKKey
	Key    *string
	Expiry *time.Time
}

// MobileKeyParams describes one accepted mobile key for the builder. See SDKKeyParams.
type MobileKeyParams struct {
	Value  config.MobileKey
	Key    *string
	Expiry *time.Time
}

// WithSDKKey adds a server-side SDK key. It is a no-op if the value is undefined, or if the value is
// already the designated anchor. When the value is already present, the first wire name recorded for
// it wins, and the entry keeps the earlier of the two expiries. A value a payload lists twice must not
// outlive the soonest expiry that payload gives it.
func (b *AcceptedSetBuilder) WithSDKKey(p SDKKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() || p.Value == b.set.anchor {
		return b
	}
	if existing, present := b.set.sdkKeys[p.Value]; present {
		existing.Expiry = earlierExpiry(existing.Expiry, p.Expiry)
		b.set.sdkKeys[p.Value] = existing
		return b
	}
	b.set.sdkKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: p.Expiry}
	return b
}

// WithAnchor adds p.Value and designates it as the anchor. The anchor is always permanent, so
// p.Expiry is ignored. It is a no-op if the value is undefined. Unlike WithSDKKey, it overwrites an
// existing entry for the value.
func (b *AcceptedSetBuilder) WithAnchor(p SDKKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() {
		return b
	}
	b.set.sdkKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: nil}
	b.set.anchor = p.Value
	return b
}

// WithMobileKey adds a mobile key. It resolves a repeated value the same way WithSDKKey does, and it
// is a no-op for the value that is already the designated primary.
func (b *AcceptedSetBuilder) WithMobileKey(p MobileKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() || p.Value == b.set.primaryMobileKey {
		return b
	}
	if existing, present := b.set.mobileKeys[p.Value]; present {
		existing.Expiry = earlierExpiry(existing.Expiry, p.Expiry)
		b.set.mobileKeys[p.Value] = existing
		return b
	}
	b.set.mobileKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: p.Expiry}
	return b
}

// WithPrimaryMobileKey adds p.Value and designates it as the primary mobile key. The primary is
// always permanent, so p.Expiry is ignored. It is a no-op if the value is undefined.
func (b *AcceptedSetBuilder) WithPrimaryMobileKey(p MobileKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() {
		return b
	}
	b.set.mobileKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: nil}
	b.set.primaryMobileKey = p.Value
	return b
}

// WithEnvironmentID sets the environment ID. It is a no-op if the ID is undefined.
func (b *AcceptedSetBuilder) WithEnvironmentID(id config.EnvironmentID) *AcceptedSetBuilder {
	if id.Defined() {
		b.set.envID = id
	}
	return b
}

// Build validates and returns the accumulated AcceptedSet. It returns a *MalformedCredentialSetError
// if no SDK key was added, or if no anchor was designated.
func (b *AcceptedSetBuilder) Build() (AcceptedSet, error) {
	if len(b.set.sdkKeys) == 0 {
		return AcceptedSet{}, newNoSDKKeysError()
	}
	if !b.set.anchor.Defined() {
		return AcceptedSet{}, newMissingAnchorError()
	}
	return b.set, nil
}

// earlierExpiry returns whichever of the two expiries comes first. A nil expiry means the key is
// permanent, so it loses to any expiry that is set.
func earlierExpiry(current, candidate *time.Time) *time.Time {
	if current == nil {
		return candidate
	}
	if candidate == nil {
		return current
	}
	if candidate.Before(*current) {
		return candidate
	}
	return current
}
