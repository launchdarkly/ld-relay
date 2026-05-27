package relay

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
)

const (
	logMsgAutoConfEnvInitError            = "Unable to initialize auto-configured environment %q: %s"
	logMsgAutoConfUpdateUnknownEnv        = "Got auto-configuration update for environment %q but did not have previous configuration - will add"
	logMsgAutoConfDeleteUnknownEnv        = "Got auto-configuration delete message for environment %s but did not have previous configuration - ignoring"
	logMsgAutoConfReceivedAllEnvironments = "Finished processing auto-configuration data"
	logMsgKeyExpiryUnknownEnv             = "Got auto-configuration key expiry message for environment %s but did not have previous configuration - ignoring"
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

	applyExpiringPrimaries(env, params)
	env.SetAdditionalSDKKeys(params.AdditionalSDKKeys, params.ExpiringAdditionalSDKKeys)
	env.SetAdditionalMobileKeys(params.AdditionalMobileKeys, params.ExpiringAdditionalMobileKeys)
}

func (a *relayAutoConfigActions) UpdateEnvironment(params envfactory.EnvironmentParams) {
	env, err := a.r.getEnvironment(sdkauth.NewScoped(params.Identifiers.FilterKey, params.EnvID))
	if err != nil {
		a.r.loggers.Warnf(logMsgAutoConfUpdateUnknownEnv, params.Identifiers.GetDisplayName())
		return
	}

	env.SetIdentifiers(params.Identifiers)
	env.SetTTL(params.TTL)
	env.SetSecureMode(params.SecureMode)

	if params.SDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.SDKKey)
		if params.ExpiringSDKKey.Defined() {
			update = update.WithGracePeriod(params.ExpiringSDKKey.Key, params.ExpiringSDKKey.Expiration)
		}
		env.UpdateCredential(update)
	}
	if params.MobileKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.MobileKey)
		if params.ExpiringMobileKey.Defined() {
			update = update.WithGracePeriod(params.ExpiringMobileKey.Key, params.ExpiringMobileKey.Expiration)
		}
		env.UpdateCredential(update)
	}
	env.SetAdditionalSDKKeys(params.AdditionalSDKKeys, params.ExpiringAdditionalSDKKeys)
	env.SetAdditionalMobileKeys(params.AdditionalMobileKeys, params.ExpiringAdditionalMobileKeys)
}

// applyExpiringPrimaries handles the optional grace periods on the primary SDK and mobile keys when
// a freshly-added environment already arrives mid-rotation. The primary keys themselves are set via
// the EnvConfig during construction; this function only applies the deprecated predecessors.
func applyExpiringPrimaries(env relayenv.EnvContext, params envfactory.EnvironmentParams) {
	if params.ExpiringSDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.SDKKey)
		env.UpdateCredential(update.WithGracePeriod(params.ExpiringSDKKey.Key, params.ExpiringSDKKey.Expiration))
	}
	if params.ExpiringMobileKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.MobileKey)
		env.UpdateCredential(update.WithGracePeriod(params.ExpiringMobileKey.Key, params.ExpiringMobileKey.Expiration))
	}
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
