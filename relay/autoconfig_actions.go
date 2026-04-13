package relay

import (
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"
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
	}

	if params.ExpiringSDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.SDKKey)
		env.UpdateCredential(update.WithGracePeriod(params.ExpiringSDKKey.Key, params.ExpiringSDKKey.Expiration))
	}
}

func (a *relayAutoConfigActions) UpdateEnvironment(params envfactory.EnvironmentParams) {
	env, err := a.r.getEnvironment(sdkauth.NewScoped(params.Identifiers.FilterKey, params.EnvID))
	if err != nil {
		a.r.logger.Warn("got auto-configuration update for unknown environment, will add", "env", params.Identifiers.GetDisplayName())
		return
	}

	env.SetIdentifiers(params.Identifiers)
	// Refresh identifier-based indexes after identifiers change
	a.r.envsByCredential.RefreshEnvironmentIndexes(env)

	env.SetTTL(params.TTL)
	env.SetSecureMode(params.SecureMode)

	if params.MobileKey.Defined() {
		env.UpdateCredential(relayenv.NewCredentialUpdate(params.MobileKey))
	}
	if params.SDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.SDKKey)
		if params.ExpiringSDKKey.Defined() {
			update = update.WithGracePeriod(params.ExpiringSDKKey.Key, params.ExpiringSDKKey.Expiration)
		}
		env.UpdateCredential(update)
	}
}

func (a *relayAutoConfigActions) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	removed := a.r.removeEnvironment(sdkauth.NewScoped(filter, id))
	if !removed {
		a.r.logger.Warn("got auto-configuration delete message for unknown environment, ignoring", "envID", id)
	}
}

func (a *relayAutoConfigActions) ReceivedAllEnvironments() {
	a.r.logger.Info("finished processing auto-configuration data")
	a.r.setFullyConfigured(true)
}
