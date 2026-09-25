package relay

import (
	"strings"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
)

// relayAutoConfigActions is an implementation of the autoconfig.MessageHandler interface. The low-level
// autoconfig.StreamManager component, which manages the configuration stream protocol, will call the
// interface methods on this object to let us know when environments have been added or changed.
type relayAutoConfigActions struct {
	r *Relay
}

func (a *relayAutoConfigActions) AddEnvironment(params envfactory.EnvironmentParams) {
	// Since we're not holding the lock on the RelayCore, there is theoretically a race condition here
	// where an environment could be added from elsewhere after we checked in AddOrUpdateEnvironment.
	// But in reality, this method is only going to be called from a single goroutine in the auto-config
	// stream handler.
	envConfig := envfactory.NewEnvConfigFactoryForAutoConfig(a.r.config.AutoConfig).MakeEnvironmentConfig(params)
	env, _, err := a.r.addEnvironment(params.Identifiers, envConfig, nil)
	if err != nil {
		a.r.logger.Error("unable to initialize auto-configured environment", "env", params.Identifiers.GetDisplayName(), "error", err)
		return
	}

	a.reconcileCredentials(env, params)
}

// reconcileCredentials converts an auto-configuration payload into the environment's accepted
// credential set and applies it.
//
// A payload that cannot produce a valid set leaves the environment's credentials alone. The stream
// manager validates before it records the payload's version, so reaching this branch means a payload
// got past that check; keeping the previous set is the safe response either way.
func (a *relayAutoConfigActions) reconcileCredentials(env relayenv.EnvContext, params envfactory.EnvironmentParams) {
	name := params.Identifiers.GetDisplayName()

	set, rejected, err := envfactory.BuildAcceptedSet(params)
	if err != nil {
		a.r.logger.Error("malformed credential payload for auto-configured environment; keeping the previous credentials",
			"env", name, "error", err)
		return
	}
	if len(rejected) > 0 {
		a.r.logger.Warn("rejecting credentials scoped to a view; the Relay Proxy serves an entire "+
			"environment payload and cannot filter it to a view, so SDKs presenting these credentials are denied",
			"env", name, "keys", strings.Join(rejected, ", "))
	}
	env.ReconcileCredentials(set)
}

func (a *relayAutoConfigActions) UpdateEnvironment(params envfactory.EnvironmentParams) {
	env, err := a.r.getEnvironment(params.EnvID)
	if err != nil {
		a.r.logger.Warn("got auto-configuration update for unknown environment, will add", "env", params.Identifiers.GetDisplayName())
		return
	}

	env.SetIdentifiers(params.Identifiers)
	// Refresh identifier-based indexes after identifiers change
	a.r.envsByCredential.RefreshEnvironmentIndexes(env)

	env.SetTTL(params.TTL)
	env.SetSecureMode(params.SecureMode)

	a.reconcileCredentials(env, params)
}

func (a *relayAutoConfigActions) DeleteEnvironment(id config.EnvironmentID) {
	removed := a.r.removeEnvironment(id)
	if !removed {
		a.r.logger.Warn("got auto-configuration delete message for unknown environment, ignoring", "envID", id)
	}
}

func (a *relayAutoConfigActions) ReceivedAllEnvironments() {
	a.r.logger.Info("finished processing auto-configuration data")
	a.r.setFullyConfigured(true)
}
