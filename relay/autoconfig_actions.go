package relay

import (
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/autoconfig"
)

const (
	logMsgAutoConfEnvInitError     = "Unable to initialize auto-configured environment %q: %s"
	logMsgAutoConfUpdateUnknownEnv = "Got auto-configuration update for environment %q but did not have previous configuration - will add"
	logMsgAutoConfDeleteUnknownEnv = "Got auto-configuration delete message for environment %s but did not have previous configuration - ignoring"
	logMsgKeyExpiryUnknownEnv      = "Got auto-configuration key expiry message for environment %s but did not have previous configuration - ignoring"
)

type relayAutoConfigActions struct {
	r *Relay
}

func (a relayAutoConfigActions) AddEnvironment(params autoconfig.EnvironmentParams) {
	// Since we're not holding the lock on the RelayCore, there is theoretically a race condition here
	// where an environment could be added from elsewhere after we checked in AddOrUpdateEnvironment.
	// But in reality, this method is only going to be called from a single goroutine in the auto-config
	// stream handler.
	envConfig := makeEnvironmentConfig(params, a.r.config.AutoConfig)
	env, _, err := a.r.core.AddEnvironment(params.Identifiers, envConfig)
	if err != nil {
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), err)
	}

	if params.ExpiringSDKKey != "" {
		if a.r.core.GetEnvironment(params.ExpiringSDKKey) == nil {
			env.AddCredential(params.ExpiringSDKKey)
			env.DeprecateCredential(params.ExpiringSDKKey)
		}
	}
}

func (a relayAutoConfigActions) UpdateEnvironment(params autoconfig.EnvironmentParams) {
	env := a.r.core.GetEnvironment(params.EnvID)
	if env == nil {
		a.r.loggers.Warnf(logMsgAutoConfUpdateUnknownEnv, params.Identifiers.GetDisplayName())
		a.AddEnvironment(params)
		return
	}

	env.SetIdentifiers(params.Identifiers)
	env.SetTTL(params.TTL)
	env.SetSecureMode(params.SecureMode)

	var oldSDKKey config.SDKKey
	var oldMobileKey config.MobileKey
	for _, c := range env.GetCredentials() {
		switch c := c.(type) {
		case config.SDKKey:
			oldSDKKey = c
		case config.MobileKey:
			oldMobileKey = c
		}
	}

	if params.SDKKey != oldSDKKey {
		env.AddCredential(params.SDKKey)
		a.r.core.AddedEnvironmentCredential(env, params.SDKKey)
		if params.ExpiringSDKKey == oldSDKKey {
			env.DeprecateCredential(oldSDKKey)
		} else {
			a.r.core.RemovingEnvironmentCredential(oldSDKKey)
			env.RemoveCredential(oldSDKKey)
		}
	}

	if params.MobileKey != oldMobileKey {
		env.AddCredential(params.MobileKey)
		a.r.core.AddedEnvironmentCredential(env, params.MobileKey)
		a.r.core.RemovingEnvironmentCredential(oldMobileKey)
		env.RemoveCredential(oldMobileKey)
	}
}

func (a relayAutoConfigActions) DeleteEnvironment(id config.EnvironmentID) {
	env := a.r.core.GetEnvironment(id)
	if env == nil {
		a.r.loggers.Warnf(logMsgAutoConfDeleteUnknownEnv, id)
		return
	}
	a.r.core.RemoveEnvironment(env)
}

func (a relayAutoConfigActions) KeyExpired(id config.EnvironmentID, oldKey config.SDKKey) {
	env := a.r.core.GetEnvironment(id)
	if env == nil {
		a.r.loggers.Warnf(logMsgKeyExpiryUnknownEnv, id)
		return
	}
	a.r.core.RemovingEnvironmentCredential(oldKey)
	env.RemoveCredential(oldKey)
}

func makeEnvironmentConfig(params autoconfig.EnvironmentParams, autoConfProps config.AutoConfigConfig) config.EnvConfig {
	ret := config.EnvConfig{
		SDKKey:        params.SDKKey,
		MobileKey:     params.MobileKey,
		EnvID:         params.EnvID,
		Prefix:        maybeSubstituteEnvironmentID(autoConfProps.EnvDatastorePrefix, params.EnvID),
		TableName:     maybeSubstituteEnvironmentID(autoConfProps.EnvDatastoreTableName, params.EnvID),
		AllowedOrigin: autoConfProps.EnvAllowedOrigin,
		SecureMode:    params.SecureMode,
	}
	if params.TTL != 0 {
		ret.TTL = ct.NewOptDuration(params.TTL)
	}

	return ret
}

func maybeSubstituteEnvironmentID(s string, envID config.EnvironmentID) string {
	return strings.ReplaceAll(s, config.AutoConfigEnvironmentIDPlaceholder, string(envID))
}
