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

// WithSDKKey adds a server-side SDK key. It is a no-op if the value is undefined or already present
// (the first metadata recorded for a value wins).
func (b *AcceptedSetBuilder) WithSDKKey(p SDKKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() || b.set.hasSDKKey(p.Value) {
		return b
	}
	b.set.sdkKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: p.Expiry}
	return b
}

// WithAnchor adds p.Value and designates it as the anchor — the SDK key that owns the environment's
// upstream connection. The anchor is always permanent, so p.Expiry is ignored. It is a no-op if the
// value is undefined. Unlike WithSDKKey it overwrites any existing entry for the value, since
// designating the anchor takes precedence over an earlier non-anchor add.
func (b *AcceptedSetBuilder) WithAnchor(p SDKKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() {
		return b
	}
	b.set.sdkKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: nil}
	b.set.anchor = p.Value
	return b
}

// WithMobileKey adds a mobile key. It is a no-op if the value is undefined or already present.
func (b *AcceptedSetBuilder) WithMobileKey(p MobileKeyParams) *AcceptedSetBuilder {
	if !p.Value.Defined() || b.set.hasMobileKey(p.Value) {
		return b
	}
	b.set.mobileKeys[p.Value] = AcceptedKey{Key: p.Key, Expiry: p.Expiry}
	return b
}

// WithPrimaryMobileKey adds p.Value and designates it as the primary mobile key — the singular
// default (the wire's mobKey) used where one mobile key is required, e.g. event forwarding. The
// primary is always permanent, so p.Expiry is ignored. It is a no-op if the value is undefined.
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

// Build validates and returns the accumulated AcceptedSet. It returns errAcceptedSetMissingSDKKey if
// no SDK key was added, or a *MalformedCredentialSetError if no anchor was designated (via
// WithAnchor). Because WithAnchor also adds the key, a designated anchor is always among the
// accepted SDK keys.
func (b *AcceptedSetBuilder) Build() (AcceptedSet, error) {
	if len(b.set.sdkKeys) == 0 {
		return AcceptedSet{}, errAcceptedSetMissingSDKKey
	}
	if !b.set.anchor.Defined() {
		return AcceptedSet{}, newMissingAnchorError()
	}
	return b.set, nil
}
