package relay

import (
	"context"
	"encoding/json"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfigcache"
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

// cachingAutoConfigHandler wraps relayAutoConfigActions and implements autoconfig.PutContentReceiver
// to persist each put payload to the cache store when InitFromStoreFirst is enabled.
type cachingAutoConfigHandler struct {
	*relayAutoConfigActions
	cache autoconfigcache.Store
}

var _ autoconfig.PutContentReceiver = (*cachingAutoConfigHandler)(nil)

func (c *cachingAutoConfigHandler) ReceivedPutContent(content autoconfig.PutContent) {
	if c.cache == nil {
		return
	}
	raw, err := json.Marshal(content)
	if err != nil {
		c.r.loggers.Warnf("Failed to marshal AutoConfig put content for cache: %v", err)
		return
	}
	if err := c.cache.Set(context.Background(), raw); err != nil {
		c.r.loggers.Warnf("Failed to write AutoConfig cache: %v", err)
	}
}

// applyPutContentToHandler replays a put payload onto the handler (e.g. after loading from cache on startup).
func applyPutContentToHandler(handler autoconfig.MessageHandler, content autoconfig.PutContent) {
	for id, rep := range content.Environments {
		if id != rep.EnvID {
			continue
		}
		handler.AddEnvironment(rep.ToParams())
	}
	for id, filter := range content.Filters {
		handler.AddFilter(filter.ToParams(id))
	}
	handler.ReceivedAllEnvironments()
}

// loadAutoConfigFromStoreAndApply reads the cached AutoConfig from the store, unmarshals it, and applies it
// to the given handler. Returns true if a valid snapshot was loaded and applied.
func loadAutoConfigFromStoreAndApply(store autoconfigcache.Store, handler autoconfig.MessageHandler, loggers ldlog.Loggers) bool {
	data, err := store.Get(context.Background())
	if err != nil {
		loggers.Warnf("AutoConfig cache read failed (will rely on stream): %v", err)
		return false
	}
	if len(data) == 0 {
		return false
	}
	var content autoconfig.PutContent
	if err := json.Unmarshal(data, &content); err != nil {
		loggers.Warnf("AutoConfig cache data invalid (will rely on stream): %v", err)
		return false
	}
	applyPutContentToHandler(handler, content)
	return true
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
	}

	if params.ExpiringSDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(params.SDKKey)
		env.UpdateCredential(update.WithGracePeriod(params.ExpiringSDKKey.Key, params.ExpiringSDKKey.Expiration))
	}
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
		a.r.loggers.Warnf(logMsgAutoConfDeleteUnknownEnv, id)
	}
}

func (a *relayAutoConfigActions) ReceivedAllEnvironments() {
	a.r.loggers.Info(logMsgAutoConfReceivedAllEnvironments)
	a.r.setFullyConfigured(true)
}
