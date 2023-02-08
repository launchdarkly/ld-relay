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

	// DeprecatedSDKKey is an additional SDK key that should also be allowed (but not surfaced as
	// the canonical one), or "" if none. The expiry time is not represented here; it is managed
	// by lower-level components.
	ExpiringSDKKey config.SDKKey

	// TTL is the cache TTL for PHP clients.
	TTL time.Duration

	// SecureMode is true if secure mode is required for this environment.
	SecureMode bool
}
