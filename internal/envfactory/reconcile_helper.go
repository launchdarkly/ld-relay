package envfactory

import (
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/util"
)

// BuildAcceptedSet converts an EnvironmentParams into the AcceptedSet needed by
// EnvContext.ReconcileCredentials.
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
// An error is returned (with an empty AcceptedSet) for a structurally malformed payload: an undefined
// anchor (params.SDKKey not set), a defined anchor that is absent from params.AcceptedSDKKeys, a
// defined primary mobile key (params.MobileKey) that is absent from params.AcceptedMobileKeys, a
// non-empty params.AcceptedMobileKeys with no designated primary (params.MobileKey undefined), an
// array entry with an empty value, or no usable SDK key at all. The caller must preserve the previous
// accepted state and, for RAC handlers, reconnect the stream with jitter to force a fresh put. This is
// the single home for the anchor invariant.
//
// Keys scoped to a view are filtered out here rather than at authentication time, making this the
// single funnel for both RAC and the offline archive. The second return value names the keys that were
// dropped, so callers can log what they lost. An SDK presenting one of them gets a 401: the key is
// simply absent from the lookup map.
func BuildAcceptedSet(params EnvironmentParams) (credential.AcceptedSet, []string, error) {
	anchor := params.SDKKey
	b := credential.NewAcceptedSetBuilder().WithEnvironmentID(params.EnvID)
	var rejected []string

	// Add every accepted SDK key, designating the anchor as we encounter it. WithAnchor both adds and
	// designates, and forces the anchor permanent — so a payload that (wrongly) carries an expiry on
	// the anchor's own entry cannot demote it. An undefined anchor never matches a (defined) array
	// value, so it is never designated and Build rejects the payload.
	//
	// Entries with an empty value are structurally malformed: relay would silently accept them but
	// they can never authenticate any SDK. Reject loudly rather than produce a credential-short env.
	anchorInArray := false
	for _, k := range params.AcceptedSDKKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, nil, credential.NewEmptyCredentialError("sdkKeys", k.Key)
		}
		switch {
		// A marker on the anchor's own entry is disregarded: dropping the designated key would take the
		// whole environment down, and the backend forbids views on a default key in the first place.
		case k.Value == anchor:
			anchorInArray = true
			b.WithAnchor(credential.SDKKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key)})
		case k.HasViews:
			rejected = append(rejected, k.Key)
		default:
			b.WithSDKKey(credential.SDKKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key), Expiry: util.PtrOrNil(k.Expiry)})
		}
	}

	// The anchor must be one of the accepted SDK keys: the backend lists it in sdkKeys[] (and ToParams
	// synthesizes it into the array for old-format payloads). A defined anchor absent from the array is
	// a structurally malformed payload — reject it.
	if anchor.Defined() && !anchorInArray {
		return credential.AcceptedSet{}, nil, credential.NewAnchorNotInSetError()
	}

	// Add every accepted mobile key, designating the primary as we encounter it. Like the anchor,
	// WithPrimaryMobileKey forces the primary permanent, so an expiry the payload may carry on the
	// primary's own entry cannot demote it.
	primaryMobileInArray := false
	for _, k := range params.AcceptedMobileKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, nil, credential.NewEmptyCredentialError("mobileKeys", k.Key)
		}
		switch {
		// Like the anchor, a marker on the primary's own entry is disregarded rather than honored.
		case k.Value == params.MobileKey:
			primaryMobileInArray = true
			b.WithPrimaryMobileKey(credential.MobileKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key)})
		case k.HasViews:
			rejected = append(rejected, k.Key)
		default:
			b.WithMobileKey(credential.MobileKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key), Expiry: util.PtrOrNil(k.Expiry)})
		}
	}

	// The primary mobile key, when the environment has one, must be in mobileKeys[] — the mobile
	// analogue of the anchor invariant above. A defined mobKey absent from the array is malformed:
	// without this guard the primary would be silently left undesignated, clearing it on reconcile and
	// breaking event forwarding. (An undefined mobKey is valid — a server-side-only environment.)
	if params.MobileKey.Defined() && !primaryMobileInArray {
		return credential.AcceptedSet{}, nil, credential.NewPrimaryMobileKeyNotInSetError()
	}

	// A non-empty mobileKeys[] with no designated primary (undefined mobKey) is malformed: the reconcile
	// would clear the rotator's primary mobile key without a repoint, so event forwarding would keep
	// using the previous (possibly revoked) primary — silent misattribution rather than a loud
	// rejection. (No mobile keys at all — empty array and undefined mobKey — stays valid: a
	// server-side-only environment. Old-format payloads synthesize the array from mobKey only, so an
	// undefined mobKey yields an empty array and is unaffected.)
	if len(params.AcceptedMobileKeys) > 0 && !params.MobileKey.Defined() {
		return credential.AcceptedSet{}, nil, credential.NewPrimaryMobileKeyMissingError()
	}

	set, err := b.Build()
	if err != nil {
		return credential.AcceptedSet{}, nil, err
	}
	return set, rejected, nil
}
