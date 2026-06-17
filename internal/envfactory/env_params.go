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

	// SDKKey is the environment's SDK key; if there is more than one active key, it is the latest.
	SDKKey config.SDKKey

	// MobileKey is the environment's mobile key.
	MobileKey config.MobileKey

	// ExpiringSDKKey is an additional SDK key that should also be allowed (but not surfaced as
	// the canonical one).
	ExpiringSDKKey ExpiringSDKKey

	// AcceptedSDKKeys is the full accepted set of SDK keys for this environment, including the
	// anchor. Populated only when the source (RAC or offline archive) emits the sdkKeys array.
	// Nil for old-format payloads that carry only the singular sdkKey field.
	AcceptedSDKKeys []AcceptedSDKKey

	// AcceptedMobileKeys is the full accepted set of mobile keys for this environment. Populated
	// only when the source emits the mobileKeys array; nil for old-format payloads.
	AcceptedMobileKeys []AcceptedMobileKey

	// TTL is the cache TTL for PHP clients.
	TTL time.Duration

	// SecureMode is true if secure mode is required for this environment.
	SecureMode bool
}

// AcceptedSDKKey is one entry in the accepted SDK key set for an environment.
// Expiry is zero if the key is permanent.
type AcceptedSDKKey struct {
	Key    string
	Value  config.SDKKey
	Expiry time.Time
}

// AcceptedMobileKey is one entry in the accepted mobile key set for an environment.
// Expiry is zero if the key is permanent.
type AcceptedMobileKey struct {
	Key    string
	Value  config.MobileKey
	Expiry time.Time
}

type ExpiringSDKKey struct {
	Key        config.SDKKey
	Expiration time.Time
}

func (e ExpiringSDKKey) Defined() bool {
	return e.Key.Defined()
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
