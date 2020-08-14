package relayenv

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/core/internal/util"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/streams"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// LogNameMode is used in NewEnvContext to determine whether the environment's log messages should be
// tagged by SDK key or by environment ID.
type LogNameMode bool

const (
	// LogNameIsSDKKey means the log messages should be tagged with the last 4 characters of the SDK key.
	// This is the default behavior for the Relay Proxy.
	LogNameIsSDKKey LogNameMode = false

	// LogNameIsEnvID means the log messages should be tagged with the last 4 characters of the environment
	// ID. This is the default behavior for Relay Proxy Enterprise when running in auto-configuration mode,
	// where we always know the environment ID but the SDK key is subject to change.
	LogNameIsEnvID LogNameMode = true
)

func errInitPublisher(err error) error {
	return fmt.Errorf("failed to initialize event publisher: %w", err)
}

func errInitMetrics(err error) error {
	return fmt.Errorf("failed to initialize metrics for environment: %w", err)
}

type envContextImpl struct {
	mu               sync.RWMutex
	clients          map[config.SDKKey]sdks.LDClientContext
	storeAdapter     *store.SSERelayDataStoreAdapter
	loggers          ldlog.Loggers
	credentials      map[config.SDKCredential]bool // true if not deprecated
	name             string
	secureMode       bool
	envStreams       *streams.EnvStreams
	streamProviders  []streams.StreamProvider
	handlers         map[streams.StreamProvider]map[config.SDKCredential]http.Handler
	jsContext        JSClientContext
	eventDispatcher  *events.EventDispatcher
	sdkConfig        ld.Config
	sdkClientFactory sdks.ClientFactoryFunc
	metricsManager   *metrics.Manager
	metricsEnv       *metrics.EnvironmentManager
	metricsEventPub  events.EventPublisher
	globalLoggers    ldlog.Loggers
	ttl              time.Duration
	initErr          error
}

// Implementation of the DataStoreQueries interface that the streams package uses as an abstraction of
// accessing our data store.
type envContextStoreQueries struct {
	context *envContextImpl
}

// NewEnvContext creates the internal implementation of EnvContext.
//
// It immediately begins trying to initialize the SDK client for this environment. Since that might
// take a while, it is done on a separate goroutine. The EnvContext instance is returned immediately
// in an uninitialized state, and once the SDK client initialization has either succeeded or failed,
// the same EnvContext will be pushed to the channel readyCh.
//
// NewEnvContext can also immediately return an error, with a nil EnvContext, if the configuration is
// invalid.
func NewEnvContext(
	envName string,
	envConfig config.EnvConfig,
	allConfig config.Config,
	clientFactory sdks.ClientFactoryFunc,
	dataStoreFactory interfaces.DataStoreFactory,
	streamProviders []streams.StreamProvider,
	jsClientContext JSClientContext,
	metricsManager *metrics.Manager,
	userAgent string,
	logNameMode LogNameMode,
	loggers ldlog.Loggers,
	readyCh chan<- EnvContext,
) (EnvContext, error) {
	var thingsToCleanUp util.CleanupTasks // keeps track of partially constructed things in case we exit early
	defer thingsToCleanUp.Run()

	envLoggers := loggers
	envLoggers.SetPrefix(makeLogPrefix(logNameMode, envConfig.SDKKey, envConfig.EnvID))
	envLoggers.SetMinLevel(
		envConfig.LogLevel.GetOrElse(
			allConfig.Main.LogLevel.GetOrElse(ldlog.Info),
		),
	)

	httpConfig, err := httpconfig.NewHTTPConfig(allConfig.Proxy, envConfig.SDKKey, userAgent, loggers)
	if err != nil {
		return nil, err
	}

	credentials := make(map[config.SDKCredential]bool, 3)
	credentials[envConfig.SDKKey] = true
	if envConfig.MobileKey != "" {
		credentials[envConfig.MobileKey] = true
	}
	if envConfig.EnvID != "" {
		credentials[envConfig.EnvID] = true
	}

	envContext := &envContextImpl{
		name:             envName,
		clients:          make(map[config.SDKKey]sdks.LDClientContext),
		credentials:      credentials,
		loggers:          envLoggers,
		secureMode:       envConfig.SecureMode,
		streamProviders:  streamProviders,
		handlers:         make(map[streams.StreamProvider]map[config.SDKCredential]http.Handler),
		jsContext:        jsClientContext,
		sdkClientFactory: clientFactory,
		metricsManager:   metricsManager,
		globalLoggers:    loggers,
		ttl:              envConfig.TTL.GetOrElse(0),
	}

	envStreams := streams.NewEnvStreams(
		streamProviders,
		envContextStoreQueries{envContext},
		allConfig.Main.HeartbeatInterval.GetOrElse(config.DefaultHeartbeatInterval),
		envLoggers,
	)
	envContext.envStreams = envStreams
	thingsToCleanUp.AddCloser(envStreams)

	for c := range credentials {
		envStreams.AddCredential(c)
	}
	for _, sp := range streamProviders {
		handlers := make(map[config.SDKCredential]http.Handler)
		for c := range credentials {
			h := sp.Handler(c)
			if h != nil {
				handlers[c] = h
			}
		}
		envContext.handlers[sp] = handlers
	}

	storeAdapter := store.NewSSERelayDataStoreAdapter(dataStoreFactory, envStreams)
	envContext.storeAdapter = storeAdapter

	var eventDispatcher *events.EventDispatcher
	if allConfig.Events.SendEvents {
		envLoggers.Info("Proxying events for this environment")
		eventDispatcher = events.NewEventDispatcher(envConfig.SDKKey, envConfig.MobileKey, envConfig.EnvID,
			envLoggers, allConfig.Events, httpConfig, storeAdapter)
	}
	envContext.eventDispatcher = eventDispatcher

	streamURI := allConfig.Main.StreamURI.String()
	if streamURI == "" {
		streamURI = config.DefaultStreamURI
	}
	eventsURI := allConfig.Events.EventsURI.String()
	if eventsURI == "" {
		eventsURI = config.DefaultEventsURI
	}

	var em *metrics.EnvironmentManager
	if metricsManager != nil {
		eventsPublisher, err := events.NewHTTPEventPublisher(envConfig.SDKKey, httpConfig, envLoggers,
			events.OptionURI(eventsURI))
		if err != nil {
			return nil, errInitPublisher(err)
		}
		thingsToCleanUp.AddFunc(eventsPublisher.Close)
		envContext.metricsEventPub = eventsPublisher

		em, err = metricsManager.AddEnvironment(envName, eventsPublisher)
		if err != nil {
			return nil, errInitMetrics(err)
		}
	}
	envContext.metricsEnv = em
	thingsToCleanUp.AddFunc(func() { metricsManager.RemoveEnvironment(em) })

	envContext.sdkConfig = ld.Config{
		DataSource: ldcomponents.StreamingDataSource().BaseURI(streamURI),
		DataStore:  storeAdapter,
		Events:     ldcomponents.SendEvents().BaseURI(eventsURI),
		HTTP:       httpConfig.SDKHTTPConfigFactory,
		Logging:    ldcomponents.Logging().Loggers(envLoggers),
	}

	// Connecting may take time, so do this in parallel
	go envContext.startSDKClient(envConfig.SDKKey, readyCh, allConfig.Main.IgnoreConnectionErrors)

	thingsToCleanUp.Clear() // we've succeeded so we do not want to throw away these things

	return envContext, nil
}

func (c *envContextImpl) startSDKClient(sdkKey config.SDKKey, readyCh chan<- EnvContext, suppressErrors bool) {
	client, err := c.sdkClientFactory(sdkKey, c.sdkConfig)
	if err == nil {
		c.mu.Lock()
		c.clients[sdkKey] = client
		c.mu.Unlock()
	}

	if err != nil {
		c.initErr = err
		if suppressErrors {
			c.globalLoggers.Warnf("Ignoring error initializing LaunchDarkly client for %q: %+v", c.name, err)
		} else {
			c.globalLoggers.Errorf("Error initializing LaunchDarkly client for %q: %+v", c.name, err)
			if readyCh != nil {
				readyCh <- c
			}
			return
		}
	} else {
		c.globalLoggers.Infof("Initialized LaunchDarkly client for %q", c.name)
	}
	if readyCh != nil {
		readyCh <- c
	}
}

func (c *envContextImpl) GetName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.name
}

func (c *envContextImpl) SetName(newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if newName == c.name {
		return
	}
	c.name = newName
}

func (c *envContextImpl) GetCredentials() []config.SDKCredential {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ret := make([]config.SDKCredential, 0, len(c.credentials))
	for c, nonDeprecated := range c.credentials {
		if nonDeprecated {
			ret = append(ret, c)
		}
	}
	return ret
}

func (c *envContextImpl) AddCredential(newCredential config.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.credentials[newCredential]; found {
		return
	}
	c.credentials[newCredential] = true
	c.envStreams.AddCredential(newCredential)

	// A new SDK key means 1. we should start a new SDK client, 2. we should tell all event forwarding
	// components that use an SDK key to use the new one. A new mobile key does not require starting a
	// new SDK client, but does requiring updating any event forwarding components that use a mobile key.
	switch key := newCredential.(type) {
	case config.SDKKey:
		go c.startSDKClient(key, nil, false)
		if c.metricsEventPub != nil { // metrics event publisher always uses SDK key
			c.metricsEventPub.ReplaceCredential(key)
		}
		if c.eventDispatcher != nil {
			c.eventDispatcher.ReplaceCredential(key)
		}
	case config.MobileKey:
		if c.eventDispatcher != nil {
			c.eventDispatcher.ReplaceCredential(key)
		}
	}
}

func (c *envContextImpl) RemoveCredential(oldCredential config.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.credentials[oldCredential]; found {
		delete(c.credentials, oldCredential)
		c.envStreams.RemoveCredential(oldCredential)
		if sdkKey, ok := oldCredential.(config.SDKKey); ok {
			// The SDK client instance is tied to the SDK key, so get rid of it
			if client := c.clients[sdkKey]; client != nil {
				delete(c.clients, sdkKey)
				_ = client.Close()
			}
		}
	}
}

func (c *envContextImpl) DeprecateCredential(credential config.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.credentials[credential]; found {
		c.credentials[credential] = false
	}
}

func (c *envContextImpl) GetClient() sdks.LDClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// There might be multiple clients if there's an expiring SDK key. Find the SDK key that has a true
	// value in our map (meaning it's not deprecated) and return that client.
	for cred, valid := range c.credentials {
		if sdkKey, ok := cred.(config.SDKKey); ok && valid {
			return c.clients[sdkKey]
		}
	}
	return nil
}

func (c *envContextImpl) GetStore() interfaces.DataStore {
	return c.storeAdapter.GetStore()
}

func (c *envContextImpl) GetLoggers() ldlog.Loggers {
	return c.loggers
}

func (c *envContextImpl) GetStreamHandler(streamProvider streams.StreamProvider, credential config.SDKCredential) http.Handler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	h := c.handlers[streamProvider][credential]
	if h == nil {
		return http.HandlerFunc(invalidStreamHandler)
	}
	return h
}

func invalidStreamHandler(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

func (c *envContextImpl) GetEventDispatcher() *events.EventDispatcher {
	return c.eventDispatcher
}

func (c *envContextImpl) GetJSClientContext() JSClientContext {
	return c.jsContext
}

func (c *envContextImpl) GetMetricsContext() context.Context {
	if c.metricsEnv == nil {
		return context.Background()
	}
	return c.metricsEnv.GetOpenCensusContext()
}

func (c *envContextImpl) GetTTL() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.ttl
}

func (c *envContextImpl) SetTTL(newTTL time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ttl = newTTL
}

func (c *envContextImpl) GetInitError() error {
	return c.initErr
}

func (c *envContextImpl) IsSecureMode() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.secureMode
}

func (c *envContextImpl) SetSecureMode(secureMode bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.secureMode = secureMode
}

func (c *envContextImpl) Close() error {
	c.mu.Lock()
	for _, client := range c.clients {
		_ = client.Close()
	}
	c.clients = make(map[config.SDKKey]sdks.LDClientContext)
	c.mu.Unlock()
	_ = c.envStreams.Close()
	if c.metricsManager != nil && c.metricsEnv != nil {
		c.metricsManager.RemoveEnvironment(c.metricsEnv)
	}
	if c.metricsEventPub != nil {
		c.metricsEventPub.Close()
	}
	return nil
}

func (q envContextStoreQueries) IsInitialized() bool {
	if s := q.context.storeAdapter.GetStore(); s != nil {
		return s.IsInitialized()
	}
	return false
}

func (q envContextStoreQueries) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	if s := q.context.storeAdapter.GetStore(); s != nil {
		return s.GetAll(kind)
	}
	return nil, nil
}

func makeLogPrefix(logNameMode LogNameMode, sdkKey config.SDKKey, envID config.EnvironmentID) string {
	name := string(sdkKey)
	if logNameMode == LogNameIsEnvID && envID != "" {
		name = string(envID)
	}
	if len(name) > 4 { // real keys are always longer than this
		name = "..." + name[len(name)-4:]
	}
	return fmt.Sprintf("[env: %s]", name)
}
