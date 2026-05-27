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

	// SDKKey is the environment's primary SDK key. Additional concurrent SDK keys are carried in
	// AdditionalSDKKeys and ExpiringAdditionalSDKKeys.
	SDKKey config.SDKKey

	// MobileKey is the environment's primary mobile key. Additional concurrent mobile keys are
	// carried in AdditionalMobileKeys and ExpiringAdditionalMobileKeys.
	MobileKey config.MobileKey

	// ExpiringSDKKey is the predecessor of SDKKey during a rotation grace period, if any.
	ExpiringSDKKey ExpiringSDKKey

	// ExpiringMobileKey is the predecessor of MobileKey during a rotation grace period, if any.
	ExpiringMobileKey ExpiringMobileKey

	// AdditionalSDKKeys is the set of concurrent SDK keys with no per-key expiry.
	AdditionalSDKKeys []config.SDKKey

	// ExpiringAdditionalSDKKeys is the subset of concurrent SDK keys that carry a per-key expiry.
	ExpiringAdditionalSDKKeys map[config.SDKKey]time.Time

	// AdditionalMobileKeys is the set of concurrent mobile keys with no per-key expiry.
	AdditionalMobileKeys []config.MobileKey

	// ExpiringAdditionalMobileKeys is the subset of concurrent mobile keys that carry a per-key expiry.
	ExpiringAdditionalMobileKeys map[config.MobileKey]time.Time

	// TTL is the cache TTL for PHP clients.
	TTL time.Duration

	// SecureMode is true if secure mode is required for this environment.
	SecureMode bool
}

type ExpiringSDKKey struct {
	Key        config.SDKKey
	Expiration time.Time
}

func (e ExpiringSDKKey) Defined() bool {
	return e.Key.Defined()
}

type ExpiringMobileKey struct {
	Key        config.MobileKey
	Expiration time.Time
}

func (e ExpiringMobileKey) Defined() bool {
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
