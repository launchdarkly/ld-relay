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

	// WithPrimarySDKKey both adds the anchor and designates it. If anchor is undefined it is a
	// no-op, leaving the set with no designated anchor; Build then returns a
	// *MalformedCredentialSetError. The builder de-duplicates, so re-adding the anchor in the loop
	// below is safe.
	b := credential.NewAcceptedSetBuilder().
		WithEnvironmentID(params.EnvID).
		WithPrimarySDKKey(anchor)

	for _, k := range params.AcceptedSDKKeys {
		// The anchor is added permanently above; skip it so a payload that (wrongly) carries an
		// expiry on the anchor's own entry can never demote it to an expiring key.
		if k.Value == anchor {
			continue
		}
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

	// Designate the primary mobile key (the wire's mobKey). De-duplicated against the array above.
	b.WithPrimaryMobileKey(params.MobileKey)

	set, err := b.Build()
	if err != nil {
		return credential.AcceptedSet{}, anchor, err
	}
	return set, anchor, nil
}
