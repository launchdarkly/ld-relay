package relay

import (
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
)

const (
	logMsgAutoConfEnvInitError            = "Unable to initialize auto-configured environment %q: %s"
	logMsgAutoConfUpdateUnknownEnv        = "Got auto-configuration update for environment %q but did not have previous configuration - will add"
	logMsgAutoConfDeleteUnknownEnv        = "Got auto-configuration delete message for environment %s but did not have previous configuration - ignoring"
	logMsgAutoConfReceivedAllEnvironments = "Finished processing auto-configuration data"

	logMsgViewScopedKeysRejected = "Environment %q: rejecting credentials scoped to a view: %s." +
		" The Relay Proxy serves the entire environment payload and cannot filter it to a view," +
		" so SDKs presenting these credentials will be denied."
)

// logViewScopedKeys reports the view-scoped credentials that BuildAcceptedSet filtered out of a
// payload.
func logViewScopedKeys(loggers ldlog.Loggers, envName string, rejected []string) {
	if len(rejected) > 0 {
		loggers.Warnf(logMsgViewScopedKeysRejected, envName, strings.Join(rejected, ", "))
	}
}

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

	set, rejected, buildErr := envfactory.BuildAcceptedSet(params)
	if buildErr != nil {
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), buildErr)
		return
	}
	logViewScopedKeys(a.r.loggers, params.Identifiers.GetDisplayName(), rejected)
	env.ReconcileCredentials(set)
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

	set, rejected, buildErr := envfactory.BuildAcceptedSet(params)
	if buildErr != nil {
		// Already validated in StreamManager; keep the previous credentials if it somehow fails.
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), buildErr)
		return
	}
	logViewScopedKeys(a.r.loggers, params.Identifiers.GetDisplayName(), rejected)
	env.ReconcileCredentials(set)
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
