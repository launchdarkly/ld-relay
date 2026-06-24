package envfactory

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
)

// BuildAcceptedSet converts an EnvironmentParams into the AcceptedSet and anchor
// credential needed by EnvContext.ReconcileCredentials.
//
// Credential identity is keyed by value (the secret string), not by key (the human-readable
// identifier). A rename — same value, different identifier — therefore produces the same
// AcceptedSet as if nothing changed.
//
// Expiry comes from AcceptedSDKKey.Expiry / AcceptedMobileKey.Expiry (the arrays). The legacy
// sdkKey.expiring wire slot is never consulted here — relay trusts the array.
//
// The anchor (params.SDKKey) is added and designated as the primary SDK key, and the primary
// mobile key (params.MobileKey) is added and designated, in addition to the full accepted arrays.
// The builder de-duplicates by value, so an anchor or primary mobile key that also appears in its
// array is added only once.
//
// If no anchor is designated — params.SDKKey is undefined — no set is built and a
// *credential.MalformedCredentialSetError is returned with an empty AcceptedSet. The caller must
// preserve the previous accepted state and, for RAC handlers, reconnect the stream with jitter to
// force a fresh put. Structural validation of the wire payload (undefined credentials, an anchor
// absent from the array) happens upstream when the payload is parsed into params.
func BuildAcceptedSet(params EnvironmentParams) (credential.AcceptedSet, config.SDKKey, error) {
	anchor := params.SDKKey

	// WithPrimarySDKKey / WithPrimaryMobileKey each add the key and designate it (the anchor and the
	// wire's mobKey, respectively). An undefined key makes the call a no-op, so an undefined anchor
	// leaves the set with no designated anchor and Build returns a *MalformedCredentialSetError.
	b := credential.NewAcceptedSetBuilder().
		WithEnvironmentID(params.EnvID).
		WithPrimarySDKKey(anchor).
		WithPrimaryMobileKey(params.MobileKey)

	// Add the remaining accepted keys. The builder de-duplicates by value, so the anchor and the
	// primary mobile key — already added permanently above — are ignored when they reappear in
	// their arrays. That also defends the anchor-never-expiring invariant: a payload that (wrongly)
	// carries an expiry on the anchor's own entry cannot demote it, because the permanent anchor is
	// already present.
	for _, k := range params.AcceptedSDKKeys {
		if k.Expiry.IsZero() {
			b.WithSDKKey(k.Value)
		} else {
			b.WithExpiringSDKKey(k.Value, k.Expiry)
		}
	}

	for _, k := range params.AcceptedMobileKeys {
		if k.Expiry.IsZero() {
			b.WithMobileKey(k.Value)
		} else {
			b.WithExpiringMobileKey(k.Value, k.Expiry)
		}
	}

	set, err := b.Build()
	if err != nil {
		return credential.AcceptedSet{}, anchor, err
	}
	return set, anchor, nil
}
