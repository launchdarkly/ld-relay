package envfactory

import (
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
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

	// SDKKey is the environment's anchor SDK key (the wire's sdkKey.value).
	SDKKey config.SDKKey

	// MobileKey is the environment's mobile key.
	MobileKey config.MobileKey

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

func (e EnvironmentParams) WithFilter(key config.FilterKey) EnvironmentParams {
	e.Identifiers.FilterKey = key
	return e
}

type FilterParams struct {
	ProjKey string
	ID      config.FilterID
	Key     config.FilterKey
}
