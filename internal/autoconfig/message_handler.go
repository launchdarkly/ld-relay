package autoconfig

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"
)

// MessageHandler defines the methods that StreamManager will call when it receives messages
// from the auto-configuration stream.
type MessageHandler interface {
	// AddEnvironment is called when the stream has provided a configuration for an environment
	// that StreamManager has not seen before. This can happen due to either a "put" or a "patch".
	AddEnvironment(params envfactory.EnvironmentParams)

	// UpdateEnvironment is called when the stream has provided a new configuration for an
	// existing environment. This can happen due to either a "put" or a "patch".
	UpdateEnvironment(params envfactory.EnvironmentParams)

	// ReceivedAllEnvironments is called when StreamManager has received a "put" event and has
	// finished calling AddEnvironment or UpdateEnvironment for every environment in the list (and
	// DeleteEnvironment for any previously existing environments that are no longer in the list).
	// We use this at startup time to determine when Relay has acquired a complete configuration.
	ReceivedAllEnvironments()

	// DeleteEnvironment is called when an environment should be removed, due to either a "delete"
	// message, or a "put" that no longer contains that environment.
	DeleteEnvironment(id config.EnvironmentID)

	// KeyExpired is called when a key that was previously provided in EnvironmentParams.ExpiringSDKKey
	// has now expired. Relay should disconnect any clients currently using that key and reject any
	// future requests that use it.
	KeyExpired(id config.EnvironmentID, oldKey config.SDKKey)
}
