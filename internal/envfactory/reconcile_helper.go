package envfactory

import (
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/util"
)

// BuildAcceptedSet converts EnvironmentParams into the AcceptedSet that
// EnvContext.ReconcileCredentials needs. It is the single home for the anchor invariant, for both RAC
// and the offline archive.
//
// Credential identity is the value (the secret), not the wire identifier, so a rename produces an
// unchanged AcceptedSet.
//
// Expiry comes from the arrays. The legacy sdkKey.expiring wire slot is never consulted.
//
// On a structurally malformed payload it returns a *credential.MalformedCredentialSetError and an
// empty set. The caller must keep the previous accepted state, and RAC handlers must also reconnect
// the stream with jitter.
//
// It filters out keys scoped to a view and names them in the second return value, so callers can log
// what they dropped. An SDK presenting one gets a 401, because the key is absent from the lookup map.
func BuildAcceptedSet(params EnvironmentParams) (credential.AcceptedSet, []string, error) {
	anchor := params.SDKKey
	b := credential.NewAcceptedSetBuilder().WithEnvironmentID(params.EnvID)
	var rejected []string

	// Add every accepted SDK key, designating the anchor on the way. WithAnchor forces the anchor
	// permanent, so a payload cannot demote it with an expiry on the anchor's own entry. An undefined
	// anchor never matches an array value, so Build rejects the payload.
	//
	// An entry with an empty value can never authenticate any SDK, so reject it.
	anchorInArray := false
	for _, k := range params.AcceptedSDKKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, nil, credential.NewEmptyCredentialError("sdkKeys", k.Key)
		}
		switch {
		// A view marker on the anchor's own entry is disregarded: dropping the designated key would
		// take the whole environment down, and the backend forbids views on a default key.
		case k.Value == anchor:
			anchorInArray = true
			b.WithAnchor(credential.SDKKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key)})
		case k.HasViews:
			rejected = append(rejected, k.Key)
		default:
			b.WithSDKKey(credential.SDKKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key), Expiry: util.PtrOrNil(k.Expiry)})
		}
	}

	// The anchor must be one of the accepted SDK keys: the backend lists it in sdkKeys[], and ToParams
	// synthesizes it into the array for old-format payloads.
	if anchor.Defined() && !anchorInArray {
		return credential.AcceptedSet{}, nil, credential.NewAnchorNotInSetError()
	}

	// Add every accepted mobile key, designating the primary on the way. WithPrimaryMobileKey forces
	// the primary permanent, as WithAnchor does for the anchor.
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

	// A defined primary mobile key must be in mobileKeys[], the mobile analogue of the anchor invariant
	// above. Without this guard the primary stays undesignated, which breaks event forwarding.
	if params.MobileKey.Defined() && !primaryMobileInArray {
		return credential.AcceptedSet{}, nil, credential.NewPrimaryMobileKeyNotInSetError()
	}

	// mobileKeys[] with no designated primary is malformed. See NewPrimaryMobileKeyMissingError.
	// An empty array with an undefined mobKey stays valid: a server-side-only environment.
	if len(params.AcceptedMobileKeys) > 0 && !params.MobileKey.Defined() {
		return credential.AcceptedSet{}, nil, credential.NewPrimaryMobileKeyMissingError()
	}

	set, err := b.Build()
	if err != nil {
		return credential.AcceptedSet{}, nil, err
	}
	return set, rejected, nil
}
