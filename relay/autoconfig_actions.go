package relay

import (
	"errors"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
)

const (
	logMsgAutoConfEnvInitError            = "Unable to initialize auto-configured environment %q: %s"
	logMsgAutoConfUpdateUnknownEnv        = "Got auto-configuration update for environment %q but did not have previous configuration - will add"
	logMsgAutoConfDeleteUnknownEnv        = "Got auto-configuration delete message for environment %s but did not have previous configuration - ignoring"
	logMsgAutoConfReceivedAllEnvironments = "Finished processing auto-configuration data"
	logMsgKeyExpiryUnknownEnv             = "Got auto-configuration key expiry message for environment %s but did not have previous configuration - ignoring"

	logMsgMalformedCredentialPayloadRAC     = "Malformed credential payload for environment %q — preserving previous credentials and reconnecting RAC stream: %s"
	logMsgMalformedCredentialPayloadOffline = "Malformed credential payload for offline environment %q — preserving previous credentials: %s"
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
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), err)
		return
	}

	set, _, buildErr := envfactory.BuildAcceptedSet(params)
	if buildErr != nil {
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), buildErr)
		return
	}
	env.ReconcileCredentials(set)
}

func (a *relayAutoConfigActions) UpdateEnvironment(params envfactory.EnvironmentParams) bool {
	env, err := a.r.getEnvironment(sdkauth.NewScoped(params.Identifiers.FilterKey, params.EnvID))
	if err != nil {
		a.r.loggers.Warnf(logMsgAutoConfUpdateUnknownEnv, params.Identifiers.GetDisplayName())
		return false
	}

	env.SetIdentifiers(params.Identifiers)
	env.SetTTL(params.TTL)
	env.SetSecureMode(params.SecureMode)

	set, _, buildErr := envfactory.BuildAcceptedSet(params)
	if buildErr != nil {
		var malformed *credential.MalformedCredentialSetError
		if errors.As(buildErr, &malformed) {
			a.r.loggers.Errorf(logMsgMalformedCredentialPayloadRAC, params.Identifiers.GetDisplayName(), buildErr)
			return true // signal stream restart so backend pushes a fresh put
		}
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), buildErr)
		return false
	}
	env.ReconcileCredentials(set)
	return false
}

func (a *relayAutoConfigActions) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	removed := a.r.removeEnvironment(sdkauth.NewScoped(filter, id))
	if !removed {
		a.r.loggers.Warnf(logMsgAutoConfDeleteUnknownEnv, id)
	}
}

func (a *relayAutoConfigActions) ReceivedAllEnvironments() {
	a.r.loggers.Info(logMsgAutoConfReceivedAllEnvironments)
	a.r.setFullyConfigured(true)
}
