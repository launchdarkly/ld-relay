package envfactory

import (
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/util"
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
// what they dropped.
//
// Every value must be legal in an HTTP header. Relay sends a credential in an Authorization header,
// and the SDK refuses to re-key a client with a value it cannot send, which would fail a re-anchor
// after the rotator had already moved. Refusing the payload here keeps that failure out of reach.
func BuildAcceptedSet(params EnvironmentParams) (credential.AcceptedSet, []string, error) {
	anchor := params.SDKKey
	b := credential.NewAcceptedSetBuilder().WithEnvironmentID(params.EnvID)
	var rejected []string

	// A credential value is view-scoped if ANY of its entries says so. The builder keeps the first
	// metadata recorded for a value, so testing each entry's own marker would accept a value that a
	// payload lists twice, once marked and once not, and report only the marked entry as rejected.
	viewScopedSDKKeys := make(map[config.SDKKey]bool, len(params.AcceptedSDKKeys))
	for _, k := range params.AcceptedSDKKeys {
		if k.HasViews {
			viewScopedSDKKeys[k.Value] = true
		}
	}

	// Add every accepted SDK key, designating the anchor on the way. WithAnchor forces the anchor
	// permanent, so a payload cannot demote it with an expiry on the anchor's own entry. An undefined
	// anchor never matches an array value, so Build rejects the payload.
	//
	// An entry with an empty value can never authenticate any SDK, and neither can one relay cannot
	// put in a header, so reject the payload for either.
	anchorInArray := false
	for _, k := range params.AcceptedSDKKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, nil, credential.NewEmptyCredentialError("sdkKeys", k.Key)
		}
		if !isValidHTTPHeaderValue(string(k.Value)) {
			return credential.AcceptedSet{}, nil, credential.NewInvalidCredentialCharactersError("sdkKeys", k.Key)
		}
		switch {
		// A view marker on the anchor's own entry is disregarded: dropping the designated key would
		// take the whole environment down, and the backend forbids views on a default key. This case
		// is first for that reason, so it also outranks the duplicate-value rule above.
		case k.Value == anchor:
			anchorInArray = true
			b.WithAnchor(credential.SDKKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key)})
		case viewScopedSDKKeys[k.Value]:
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

	// Mobile keys follow the same duplicate-value rule as SDK keys above.
	viewScopedMobileKeys := make(map[config.MobileKey]bool, len(params.AcceptedMobileKeys))
	for _, k := range params.AcceptedMobileKeys {
		if k.HasViews {
			viewScopedMobileKeys[k.Value] = true
		}
	}

	// Add every accepted mobile key, designating the primary on the way. WithPrimaryMobileKey forces
	// the primary permanent, as WithAnchor does for the anchor.
	primaryMobileInArray := false
	for _, k := range params.AcceptedMobileKeys {
		if !k.Value.Defined() {
			return credential.AcceptedSet{}, nil, credential.NewEmptyCredentialError("mobileKeys", k.Key)
		}
		if !isValidHTTPHeaderValue(string(k.Value)) {
			return credential.AcceptedSet{}, nil, credential.NewInvalidCredentialCharactersError("mobileKeys", k.Key)
		}
		switch {
		// Like the anchor, a marker on the primary's own entry is disregarded rather than honored.
		case k.Value == params.MobileKey:
			primaryMobileInArray = true
			b.WithPrimaryMobileKey(credential.MobileKeyParams{Value: k.Value, Key: util.PtrOrNil(k.Key)})
		case viewScopedMobileKeys[k.Value]:
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

	// mobileKeys[] with no designated primary is malformed. Refer to NewPrimaryMobileKeyMissingError.
	//
	// An empty array with an undefined mobKey is accepted, as a server-side-only environment. That
	// shape does not come from LaunchDarkly: every environment there has at least one global key of
	// each kind, so the stream always names a mobile key. It can come from an offline archive that
	// was assembled by hand, and relay can serve such an environment, so it is not refused.
	if len(params.AcceptedMobileKeys) > 0 && !params.MobileKey.Defined() {
		return credential.AcceptedSet{}, nil, credential.NewPrimaryMobileKeyMissingError()
	}

	// Build returns a zero set with its error, and callers stop at a non-nil error without reading
	// the rest, so there is nothing to unpack here.
	set, err := b.Build()
	return set, rejected, err
}

// isValidHTTPHeaderValue reports whether s can be sent as an HTTP header value. It mirrors the check
// the Go SDK applies to an SDK key, character for character, so a value this function accepts is one
// the SDK also accepts. Both range over the string, so invalid UTF-8 yields the replacement rune and
// is refused by the upper bound.
func isValidHTTPHeaderValue(s string) bool {
	for _, ch := range s {
		if ch < 32 || ch > 127 {
			return false
		}
	}
	return true
}
