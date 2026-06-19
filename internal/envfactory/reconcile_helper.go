package envfactory

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
)

// BuildAcceptedSet converts an EnvironmentParams into the AcceptedSet and anchor
// credential needed by EnvContext.ReconcileCredentials.
//
// Credential identity is keyed by value (the secret string), not by key (the human-readable
// identifier). A rename — same value, different identifier — therefore produces the same
// AcceptedSet as if nothing changed.
//
// Expiry comes from AcceptedSDKKey.Expiry / AcceptedMobileKey.Expiry (the arrays). The legacy
// sdkKey.expiring wire slot is never consulted here ("trust the array", design §6.3).
//
// If the anchor is malformed, no set is built and a *relayenv.MalformedCredentialSetError is
// returned with an empty AcceptedSet. The anchor is malformed when params.SDKKey is undefined
// or is not found in AcceptedSDKKeys by value. The caller must preserve the previous accepted
// state and, for RAC handlers, reconnect the stream with jitter to force a fresh put (design §9).
func BuildAcceptedSet(params EnvironmentParams) (relayenv.AcceptedSet, config.SDKKey, error) {
	anchor := params.SDKKey
	if !anchor.Defined() {
		// Pass an untyped nil rather than the empty config.SDKKey: a concrete zero value boxed
		// into the credential.SDKCredential interface is non-nil, which would route Error() down
		// the "not present" branch instead of the accurate "anchor SDK key is missing" branch.
		return relayenv.NewAcceptedSet(), anchor, &relayenv.MalformedCredentialSetError{Anchor: nil}
	}
	if !anchorInSDKKeys(anchor, params.AcceptedSDKKeys) {
		return relayenv.NewAcceptedSet(), anchor, &relayenv.MalformedCredentialSetError{Anchor: anchor}
	}

	set := relayenv.NewAcceptedSet().WithEnvironmentID(params.EnvID)

	for _, k := range params.AcceptedSDKKeys {
		// The anchor is always permanent (design §4.2: sdkKey always names a non-expiring key).
		// Defend the invariant here: even if a payload carries an expiry on the anchor entry,
		// add it as a permanent key so it can never be treated as a deprecated/expiring key.
		if k.Value == anchor || k.Expiry.IsZero() {
			set = set.WithSDKKey(k.Value)
		} else {
			set = set.WithExpiringSDKKey(k.Value, k.Expiry)
		}
	}

	// AcceptedSet.mobileKeys is a flat slice with no per-key expiry; expiring mobile key
	// cleanup is handled by the StepTime ticker (T1.c). Add all mobile keys unconditionally.
	for _, k := range params.AcceptedMobileKeys {
		set = set.WithMobileKey(k.Value)
	}

	return set, anchor, nil
}

// anchorInSDKKeys reports whether anchor appears in keys by value.
func anchorInSDKKeys(anchor config.SDKKey, keys []AcceptedSDKKey) bool {
	for _, k := range keys {
		if k.Value == anchor {
			return true
		}
	}
	return false
}
