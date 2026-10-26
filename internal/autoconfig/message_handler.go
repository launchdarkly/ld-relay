package autoconfig

import (
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
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

	// EnvironmentRefused is called when a "patch" gave an environment a configuration Relay cannot
	// use, so that environment is not served. reason is a short stable phrase, safe to publish: the
	// detail goes to the log instead. A later message that Relay can use clears the refusal.
	EnvironmentRefused(id config.EnvironmentID, reason string)

	// SetRefusedEnvironments is called at the end of a "put" with every environment in it that Relay
	// could not use. A "put" is the full environment set, so this replaces whatever was refused
	// before rather than adding to it, which is what retires a refusal for an environment the "put"
	// no longer mentions at all.
	SetRefusedEnvironments(refused map[config.EnvironmentID]string)

	// ClearEnvironmentRefusal is called for a "delete", to retire any refusal recorded against that
	// environment. It is separate from DeleteEnvironment because an environment that was only ever
	// refused was never applied, so nothing tracks it and no delete is dispatched for it. It is a
	// no-op when nothing was refused.
	ClearEnvironmentRefusal(id config.EnvironmentID)
}
