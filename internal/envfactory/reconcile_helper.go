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
// A *credential.MalformedCredentialSetError is returned (with an empty AcceptedSet) for a
// structurally malformed payload: an undefined anchor (params.SDKKey not set), a defined anchor that
// is absent from params.AcceptedSDKKeys, or an array entry with an empty value. The caller must
// preserve the previous accepted state and, for RAC handlers, reconnect the stream with jitter to
// force a fresh put. This is the single home for the anchor invariant.
func BuildAcceptedSet(params EnvironmentParams) (credential.AcceptedSet, config.SDKKey, error) {
	anchor := params.SDKKey

	// WithAnchor / WithPrimaryMobileKey each add the key and designate it (the anchor and the
	// wire's mobKey, respectively). An undefined key makes the call a no-op, so an undefined anchor
	// leaves the set with no designated anchor and Build returns a *MalformedCredentialSetError.
	b := credential.NewAcceptedSetBuilder().
		WithEnvironmentID(params.EnvID).
		WithAnchor(anchor).
		WithPrimaryMobileKey(params.MobileKey)

	// Validate and add the remaining accepted keys. The builder de-duplicates by value, so the
	// anchor and the primary mobile key — already added permanently above — are ignored when they
	// reappear in their arrays. That also defends the anchor-never-expiring invariant: a payload
	// that (wrongly) carries an expiry on the anchor's own entry cannot demote it.
	//
	// Entries with an empty value are structurally malformed: relay would silently accept them but
	// they can never authenticate any SDK. Reject loudly rather than produce a credential-short env.
	anchorInArray := false
	for _, k := range params.AcceptedSDKKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, anchor, credential.NewEmptyCredentialError("sdkKeys", k.Key)
		}
		if k.Value == anchor {
			anchorInArray = true
		}
		if k.Expiry.IsZero() {
			b.WithSDKKey(k.Value)
		} else {
			b.WithExpiringSDKKey(k.Value, k.Expiry)
		}
	}

	// The anchor must be one of the accepted SDK keys: the backend lists it in sdkKeys[] (and ToParams
	// synthesizes it into the array for old-format payloads). A defined anchor absent from the array is
	// a structurally malformed payload — reject it per §9 rather than letting WithAnchor above
	// silently synthesize it into the set.
	if anchor.Defined() && !anchorInArray {
		return credential.AcceptedSet{}, anchor, credential.NewAnchorNotInSetError()
	}

	primaryMobileInArray := false
	for _, k := range params.AcceptedMobileKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, anchor, credential.NewEmptyCredentialError("mobileKeys", k.Key)
		}
		if k.Value == params.MobileKey {
			primaryMobileInArray = true
		}
		if k.Expiry.IsZero() {
			b.WithMobileKey(k.Value)
		} else {
			b.WithExpiringMobileKey(k.Value, k.Expiry)
		}
	}

	// The primary mobile key, when the environment has one, must be in mobileKeys[] — the mobile
	// analogue of the anchor invariant above. A defined mobKey absent from the array is malformed:
	// without this guard the primary would be silently left undesignated, clearing it on reconcile and
	// breaking event forwarding. (An undefined mobKey is valid — a server-side-only environment.)
	if params.MobileKey.Defined() && !primaryMobileInArray {
		return credential.AcceptedSet{}, anchor, credential.NewPrimaryMobileKeyNotInSetError()
	}

	set, err := b.Build()
	if err != nil {
		return credential.AcceptedSet{}, anchor, err
	}
	return set, anchor, nil
}
