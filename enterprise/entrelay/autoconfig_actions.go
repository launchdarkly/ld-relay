package entrelay

import (
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/enterprise/autoconfig"
)

const (
	logMsgAutoConfEnvInitError     = "Unable to initialize auto-configured environment %q: %s"
	logMsgAutoConfUpdateUnknownEnv = "Got auto-configuration update for environment %q but did not have previous configuration - will add"
	logMsgAutoConfDeleteUnknownEnv = "Got auto-configuration delete message for environment %s but did not have previous configuration - ignoring"
	logMsgKeyExpiryUnknownEnv      = "Got auto-configuration key expiry message for environment %s but did not have previous configuration - ignoring"
)

// AddEnvironment is called from autoconfig.StreamManager.
func (r *RelayEnterprise) AddEnvironment(params autoconfig.EnvironmentParams) {
	// Since we're not holding the lock on the RelayCore, there is theoretically a race condition here
	// where an environment could be added from elsewhere after we checked in AddOrUpdateEnvironment.
	// But in reality, this method is only going to be called from a single goroutine in the auto-config
	// stream handler.
	envConfig := makeEnvironmentConfig(params, r.config.AutoConfig)
	env, _, err := r.core.AddEnvironment(params.Identifiers, envConfig)
	if err != nil {
		r.core.Loggers.Errorf(logMsgAutoConfEnvInitError, params.Identifiers.GetDisplayName(), err)
	}

	if params.ExpiringSDKKey != "" {
		if r.core.GetEnvironment(params.ExpiringSDKKey) == nil {
			env.AddCredential(params.ExpiringSDKKey)
			env.DeprecateCredential(params.ExpiringSDKKey)
		}
	}
}

// UpdateEnvironment is called from autoconfig.StreamManager.
func (r *RelayEnterprise) UpdateEnvironment(params autoconfig.EnvironmentParams) {
	env := r.core.GetEnvironment(params.EnvID)
	if env == nil {
		r.core.Loggers.Warnf(logMsgAutoConfUpdateUnknownEnv, params.Identifiers.GetDisplayName())
		r.AddEnvironment(params)
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
		r.core.AddedEnvironmentCredential(env, params.SDKKey)
		if params.ExpiringSDKKey == oldSDKKey {
			env.DeprecateCredential(oldSDKKey)
		} else {
			r.core.RemovingEnvironmentCredential(oldSDKKey)
			env.RemoveCredential(oldSDKKey)
		}
	}

	if params.MobileKey != oldMobileKey {
		env.AddCredential(params.MobileKey)
		r.core.AddedEnvironmentCredential(env, params.MobileKey)
		r.core.RemovingEnvironmentCredential(oldMobileKey)
		env.RemoveCredential(oldMobileKey)
	}
}

// DeleteEnvironment is called from autoconfig.StreamManager.
func (r *RelayEnterprise) DeleteEnvironment(id config.EnvironmentID) {
	env := r.core.GetEnvironment(id)
	if env == nil {
		r.core.Loggers.Warnf(logMsgAutoConfDeleteUnknownEnv, id)
		return
	}
	r.core.RemoveEnvironment(env)
}

// KeyExpired is called from autoconfig.StreamManager.
func (r *RelayEnterprise) KeyExpired(id config.EnvironmentID, oldKey config.SDKKey) {
	env := r.core.GetEnvironment(id)
	if env == nil {
		r.core.Loggers.Warnf(logMsgKeyExpiryUnknownEnv, id)
		return
	}
	r.core.RemovingEnvironmentCredential(oldKey)
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
