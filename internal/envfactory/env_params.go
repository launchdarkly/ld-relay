package envfactory

import (
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
)

// EnvironmentParams contains environment-specific information obtained from LaunchDarkly which
// will be used to configure a Relay environment in auto-configuration mode or offline mode.
//
// This is a simplified representation that does not contain all of the properties used in the
// auto-configuration or offline mode protocols, but only the ones that the core Relay logic
// needs.
type EnvironmentParams struct {
	// ID is the environment ID.
	EnvID config.EnvironmentID

	// Identifiers contains the project and environment names and keys.
	Identifiers relayenv.EnvIdentifiers

	// SDKKey is the environment's SDK key; if there is more than one active key, it is the latest.
	SDKKey config.SDKKey

	// MobileKey is the environment's mobile key.
	MobileKey config.MobileKey

	// ExpiringSDKKey is an additional SDK key that should also be allowed (but not surfaced as
	// the canonical one).
	//
	// Superseded by AcceptedSDKKeys, which carries the same key as one entry of the full set. It is
	// retained only until EnvContext.ReconcileCredentials replaces UpdateCredential, and ToParams
	// keeps both populated from the same source in the meantime.
	ExpiringSDKKey ExpiringSDKKey

	// AcceptedSDKKeys is the full accepted set of SDK keys, including the anchor. ToParams always
	// leaves it non-nil, synthesizing from the singular sdkKey field when the payload has no
	// sdkKeys array.
	AcceptedSDKKeys []AcceptedSDKKey

	// AcceptedMobileKeys is the full accepted set of mobile keys. ToParams always leaves it non-nil,
	// synthesizing from the singular mobKey field when the payload has no mobileKeys array.
	AcceptedMobileKeys []AcceptedMobileKey

	// TTL is the cache TTL for PHP clients.
	TTL time.Duration

	// SecureMode is true if secure mode is required for this environment.
	SecureMode bool
}

// AcceptedSDKKey is one entry in the accepted SDK key set for an environment.
// Expiry is zero if the key is permanent.
// HasViews is true if the SDK key is associated with a view.
type AcceptedSDKKey struct {
	Key      string
	Value    config.SDKKey
	Expiry   time.Time
	HasViews bool
}

// AcceptedMobileKey is one entry in the accepted mobile key set for an environment.
// Expiry is zero if the key is permanent.
// HasViews is true if the mobile key is associated with a view.
type AcceptedMobileKey struct {
	Key      string
	Value    config.MobileKey
	Expiry   time.Time
	HasViews bool
}

type ExpiringSDKKey struct {
	Key        config.SDKKey
	Expiration time.Time
}

func (e ExpiringSDKKey) Defined() bool {
	return e.Key.Defined()
}
