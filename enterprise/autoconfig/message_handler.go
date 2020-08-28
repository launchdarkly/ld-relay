package autoconfig

import (
	"time"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
)

// MessageHandler defines the methods that StreamManager will call when it receives messages
// from the auto-configuration stream.
type MessageHandler interface {
	// AddEnvironment is called when the stream has provided a configuration for an environment
	// that StreamManager has not seen before. This can happen due to either a "put" or a "patch".
	AddEnvironment(params EnvironmentParams)

	// UpdateEnvironment is called when the stream has provided a new configuration for an
	// existing environment. This can happen due to either a "put" or a "patch".
	UpdateEnvironment(params EnvironmentParams)

	// DeleteEnvironment is called when an environment should be removed, due to either a "delete"
	// message, or a "put" that no longer contains that environment.
	DeleteEnvironment(id config.EnvironmentID)

	// KeyExpired is called when a key that was previously provided in EnvironmentParams.ExpiringSDKKey
	// has now expired. Relay should disconnect any clients currently using that key and reject any
	// future requests that use it.
	KeyExpired(id config.EnvironmentID, oldKey config.SDKKey)
}

// EnvironmentParams contains information that MessageHandler passes to StreamHandler to describe
// an environment. This is not the same type that we use for the environment representation in the
// auto-config stream, because not all of the properties there are relevant to Relay.
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
	// the canonical one), or "" if none. StreamManager is responsible for knowing when the key
	// will expire.
	ExpiringSDKKey config.SDKKey

	// TTL is the cache TTL for PHP clients.
	TTL time.Duration

	// SecureMode is true if secure mode is required for this environment.
	SecureMode bool
}
