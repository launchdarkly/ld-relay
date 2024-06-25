package relayenv

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/store"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldeval "github.com/launchdarkly/go-server-sdk-evaluation/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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

	// By default, credentials that have an expiry date in the future (compared to when the message containing the
	// expiry was received) will be cleaned up on an interval with this granularity. This means the environment won't accept
	// connections for this credential, and it will shut down the SDK client associated with that credential.
	defaultCredentialCleanupInterval = 1 * time.Minute
)

func errInitPublisher(err error) error {
	return fmt.Errorf("failed to initialize event publisher: %w", err)
}

func errInitMetrics(err error) error {
	return fmt.Errorf("failed to initialize metrics for environment: %w", err)
}

type ConnectionMapper interface {
	AddConnectionMapping(scopedCredential sdkauth.ScopedCredential, envContext EnvContext)
	RemoveConnectionMapping(scopedCredential sdkauth.ScopedCredential)
}

// EnvContextImplParams contains the constructor parameters for NewEnvContextImpl. These have their
// own type because there are a lot of them, and many are irrelevant in tests.
type EnvContextImplParams struct {
	Identifiers                      EnvIdentifiers
	EnvConfig                        config.EnvConfig
	AllConfig                        config.Config
	ClientFactory                    sdks.ClientFactoryFunc
	DataStoreFactory                 subsystems.ComponentConfigurer[subsystems.DataStore]
	DataStoreInfo                    sdks.DataStoreEnvironmentInfo
	StreamProviders                  []streams.StreamProvider
	JSClientContext                  JSClientContext
	MetricsManager                   *metrics.Manager
	BigSegmentStoreFactory           bigsegments.BigSegmentStoreFactory
	BigSegmentSynchronizerFactory    bigsegments.BigSegmentSynchronizerFactory
	SDKBigSegmentsConfigFactory      subsystems.ComponentConfigurer[subsystems.BigSegmentsConfiguration] // set only in tests
	UserAgent                        string
	LogNameMode                      LogNameMode
	Loggers                          ldlog.Loggers
	ConnectionMapper                 ConnectionMapper
	ExpiredCredentialCleanupInterval time.Duration
}

type envContextImpl struct {
	mu                        sync.RWMutex
	clients                   map[config.SDKKey]sdks.LDClientContext
	storeAdapter              *store.SSERelayDataStoreAdapter
	loggers                   ldlog.Loggers
	identifiers               EnvIdentifiers
	secureMode                bool
	envStreams                *streams.EnvStreams
	streamProviders           []streams.StreamProvider
	handlers                  map[streams.StreamProvider]map[credential.SDKCredential]http.Handler
	jsContext                 JSClientContext
	evaluator                 ldeval.Evaluator
	eventDispatcher           *events.EventDispatcher
	bigSegmentSync            bigsegments.BigSegmentSynchronizer
	bigSegmentStore           bigsegments.BigSegmentStore
	bigSegmentsExist          bool
	sdkBigSegments            *ldstoreimpl.BigSegmentStoreWrapper
	sdkConfig                 ld.Config
	sdkClientFactory          sdks.ClientFactoryFunc
	sdkInitTimeout            time.Duration
	metricsManager            *metrics.Manager
	metricsEnv                *metrics.EnvironmentManager
	metricsEventPub           events.EventPublisher
	dataStoreInfo             sdks.DataStoreEnvironmentInfo
	globalLoggers             ldlog.Loggers
	ttl                       time.Duration
	initErr                   error
	creationTime              time.Time
	filterKey                 config.FilterKey
	keyRotator                *credential.Rotator
	stopMonitoringCredentials chan struct{}
	doneMonitoringCredentials chan struct{}
	connectionMapper          ConnectionMapper
	offline                   bool
}

// Implementation of the DataStoreQueries interface that the streams package uses as an abstraction of
// accessing our data store.
type envContextStoreQueries struct {
	context *envContextImpl
}

// Implementation of the EnvStreamUpdates interface that intercepts all updates from the SDK to the
// data store.
type envContextStreamUpdates struct {
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
	params EnvContextImplParams,
	readyCh chan<- EnvContext,
	// readyCh is a separate parameter because it's not a property of the environment itself, but
	// just part of the semantics of the constructor
) (EnvContext, error) {
	var thingsToCleanUp util.CleanupTasks // keeps track of partially constructed things in case we exit early
	defer thingsToCleanUp.Run()

	offlineMode := params.AllConfig.OfflineMode.FileDataSource != ""
	envConfig := params.EnvConfig
	allConfig := params.AllConfig

	envLoggers := params.Loggers
	logPrefix := makeLogPrefix(params.LogNameMode, envConfig.SDKKey, envConfig.EnvID)
	envLoggers.SetPrefix(logPrefix)
	envLoggers.SetMinLevel(
		envConfig.LogLevel.GetOrElse(
			allConfig.Main.LogLevel.GetOrElse(ldlog.Info),
		),
	)

	httpConfig, err := httpconfig.NewHTTPConfig(allConfig.Proxy, envConfig.SDKKey, params.UserAgent, params.Loggers)
	if err != nil {
		return nil, err
	}

	envContext := &envContextImpl{
		identifiers:               params.Identifiers,
		clients:                   make(map[config.SDKKey]sdks.LDClientContext),
		loggers:                   envLoggers,
		secureMode:                envConfig.SecureMode,
		streamProviders:           params.StreamProviders,
		handlers:                  make(map[streams.StreamProvider]map[credential.SDKCredential]http.Handler),
		jsContext:                 params.JSClientContext,
		sdkClientFactory:          params.ClientFactory,
		sdkInitTimeout:            allConfig.Main.InitTimeout.GetOrElse(config.DefaultInitTimeout),
		metricsManager:            params.MetricsManager,
		globalLoggers:             params.Loggers,
		ttl:                       envConfig.TTL.GetOrElse(0),
		dataStoreInfo:             params.DataStoreInfo,
		creationTime:              time.Now(),
		filterKey:                 params.EnvConfig.FilterKey,
		keyRotator:                credential.NewRotator(params.Loggers),
		stopMonitoringCredentials: make(chan struct{}),
		doneMonitoringCredentials: make(chan struct{}),
		connectionMapper:          params.ConnectionMapper,
		offline:                   envConfig.Offline,
	}

	envContext.keyRotator.Initialize([]credential.SDKCredential{
		envConfig.SDKKey,
		envConfig.MobileKey,
		envConfig.EnvID,
	})

	bigSegmentStoreFactory := params.BigSegmentStoreFactory
	if bigSegmentStoreFactory == nil {
		bigSegmentStoreFactory = bigsegments.DefaultBigSegmentStoreFactory
	}
	bigSegmentStore, err := bigSegmentStoreFactory(envConfig, allConfig, envLoggers)
	if err != nil {
		return nil, err
	}
	if bigSegmentStore != nil {
		thingsToCleanUp.AddCloser(bigSegmentStore)
		envContext.bigSegmentStore = bigSegmentStore

		factory := params.BigSegmentSynchronizerFactory
		if factory == nil {
			factory = bigsegments.DefaultBigSegmentSynchronizerFactory
		}
		envContext.bigSegmentSync = factory(
			httpConfig, bigSegmentStore, allConfig.Main.BaseURI.String(), allConfig.Main.StreamURI.String(),
			envConfig.EnvID, envConfig.SDKKey, envLoggers, logPrefix)
		thingsToCleanUp.AddFunc(envContext.bigSegmentSync.Close)
		segmentUpdateCh := envContext.bigSegmentSync.SegmentUpdatesCh()
		if segmentUpdateCh != nil {
			go func() {
				for range segmentUpdateCh {
					// BigSegmentSynchronizer sends to this channel after processing a batch of
					// big segment updates. The value it sends is a list of segment keys, but in
					// the current implementation, we don't care what those keys are because we'll
					// just be broadcasting a "ping" to all connected client-side SDKs. In the future
					// if we have real evaluation streams, we'll need to determine which flags should
					// be re-evaluated based on the segments.
					if envContext.sdkBigSegments != nil {
						envContext.sdkBigSegments.ClearCache()
					}
					if envContext.envStreams != nil {
						envContext.envStreams.InvalidateClientSideState()
					}
					// If we shut down the environment, the BigSegmentSynchronizer will be closed which
					// will also cause this channel to be closed, exiting this goroutine.
				}
			}()
		}
		// We deliberate do not call bigSegmentSync.Start() here because we don't want the synchronizer to
		// start until we know that at least one big segment exists. That's implemented by the
		// envContextStreamUpdates methods.
	}

	envStreams := streams.NewEnvStreams(
		params.StreamProviders,
		envContextStoreQueries{envContext},
		allConfig.Main.HeartbeatInterval.GetOrElse(config.DefaultHeartbeatInterval),
		envContext.filterKey,
		envLoggers,
	)
	envContext.envStreams = envStreams
	thingsToCleanUp.AddCloser(envStreams)

	envStreamUpdates := &envContextStreamUpdates{
		context: envContext,
	}

	allCreds := envContext.keyRotator.AllCredentials()
	for _, c := range allCreds {
		envStreams.AddCredential(c)
	}
	for _, sp := range params.StreamProviders {
		handlers := make(map[credential.SDKCredential]http.Handler)
		for _, c := range allCreds {
			h := sp.Handler(sdkauth.NewScoped(envContext.filterKey, c))
			if h != nil {
				handlers[c] = h
			}
		}
		envContext.handlers[sp] = handlers
	}

	dataStoreFactory := params.DataStoreFactory
	if dataStoreFactory == nil {
		dataStoreFactory = ldcomponents.InMemoryDataStore()
	}
	storeAdapter := store.NewSSERelayDataStoreAdapter(dataStoreFactory, envStreamUpdates)
	envContext.storeAdapter = storeAdapter

	var eventDispatcher *events.EventDispatcher
	if allConfig.Events.SendEvents {
		if offlineMode {
			envLoggers.Info("Events will be accepted for this environment, but will be discarded, since offline mode is enabled")
		} else {
			envLoggers.Info("Proxying events for this environment")
			eventLoggers := envLoggers
			eventLoggers.SetPrefix(logPrefix + " (event proxy)")
			eventDispatcher = events.NewEventDispatcher(
				envConfig.SDKKey,
				envConfig.MobileKey,
				envConfig.EnvID,
				envLoggers,
				allConfig.Events,
				httpConfig,
				storeAdapter,
				0, // 0 here means "use the default interval for any periodic cleanup task you may need to run"
			)
		}
	}
	envContext.eventDispatcher = eventDispatcher

	streamURI := allConfig.Main.StreamURI.String()   // config.ValidateConfig has ensured that this has a value
	eventsURI := allConfig.Events.EventsURI.String() // ditto

	// Unlike our SDKs, the relay proxy does not provide an option to disable
	// diagnostic events. However, we must still honor the offline mode where 0
	// outbound connections will be made.
	enableDiagnostics := !offlineMode
	var em *metrics.EnvironmentManager
	if params.MetricsManager != nil {
		if enableDiagnostics {
			pubLoggers := envLoggers
			pubLoggers.SetPrefix(logPrefix + " (usage metrics)")
			eventsPublisher, err := events.NewHTTPEventPublisher(envConfig.SDKKey, httpConfig, pubLoggers,
				events.OptionBaseURI(eventsURI))
			if err != nil {
				return nil, errInitPublisher(err)
			}
			thingsToCleanUp.AddFunc(eventsPublisher.Close)
			envContext.metricsEventPub = eventsPublisher
		}

		em, err = params.MetricsManager.AddEnvironment(params.Identifiers.GetDisplayName(), envContext.metricsEventPub)
		if err != nil {
			return nil, errInitMetrics(err)
		}
		thingsToCleanUp.AddFunc(func() { params.MetricsManager.RemoveEnvironment(em) })
	}
	envContext.metricsEnv = em

	disconnectedStatusTime := allConfig.Main.DisconnectedStatusTime.GetOrElse(config.DefaultDisconnectedStatusTime)

	dataSource := ldcomponents.StreamingDataSource()

	if params.EnvConfig.FilterKey != "" {
		dataSource.PayloadFilter(string(params.EnvConfig.FilterKey))
	}

	envContext.sdkConfig = ld.Config{
		DataSource:       dataSource,
		DataStore:        storeAdapter,
		DiagnosticOptOut: !enableDiagnostics,
		Events:           ldcomponents.SendEvents(),
		HTTP:             httpConfig.SDKHTTPConfigFactory,
		Logging: ldcomponents.Logging().
			Loggers(envLoggers).
			LogDataSourceOutageAsErrorAfter(disconnectedStatusTime),
		ServiceEndpoints: interfaces.ServiceEndpoints{
			Streaming: streamURI,
			Events:    eventsURI,
		},
	}

	// If appropriate, create the SDK subcomponent that will be used for flag evaluations. We're
	// creating and managing it separately from the full SDK instance that we'll be creating (in
	// startSDKClient) - we use the SDK instance only for talking to LaunchDarkly and populating
	// the data store, not for evaluating flags, because Relay needs to customize the evaluation
	// behavior. The other component we need for evaluations is the Evaluator, but we can't create
	// that one we get to startSDKClient because it has to be hooked up to the SDK's data store.
	if bigSegmentStore != nil {
		configFactory := params.SDKBigSegmentsConfigFactory
		if configFactory == nil {
			configFactory, err = sdks.ConfigureBigSegments(allConfig, envConfig, params.Loggers)
			if err != nil {
				return nil, err
			}
		}
		bigSegConfig, err := configFactory.Build(
			sdks.NewSimpleClientContext(string(envConfig.SDKKey), envContext.sdkConfig))
		if err != nil {
			return nil, err
		}
		if bigSegConfig != nil {
			envContext.sdkBigSegments = ldstoreimpl.NewBigSegmentStoreWrapperWithConfig(
				ldstoreimpl.BigSegmentsConfigurationProperties{
					Store:              bigSegConfig.GetStore(),
					StatusPollInterval: bigSegConfig.GetStatusPollInterval(),
					StaleAfter:         bigSegConfig.GetStaleAfter(),
					ContextCacheSize:   bigSegConfig.GetContextCacheSize(),
					ContextCacheTime:   bigSegConfig.GetContextCacheTime(),
					StartPolling:       false, // we will start it later if we see a big segment
				},
				nil,
				envLoggers,
			)
			thingsToCleanUp.AddFunc(envContext.sdkBigSegments.Close)
		}
	}

	// Connecting may take time, so do this in parallel
	go envContext.startSDKClient(envConfig.SDKKey, readyCh, allConfig.Main.IgnoreConnectionErrors)

	cleanupInterval := params.ExpiredCredentialCleanupInterval
	if cleanupInterval == 0 { // 0 means it wasn't specified; the config system disallows 0 as a valid value.
		cleanupInterval = defaultCredentialCleanupInterval
	}
	go envContext.cleanupExpiredCredentials(cleanupInterval)

	thingsToCleanUp.Clear() // we've succeeded so we do not want to throw away these things

	return envContext, nil
}

func (c *envContextImpl) cleanupExpiredCredentials(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.triggerCredentialChanges(time.Now())
		case <-c.stopMonitoringCredentials:
			close(c.doneMonitoringCredentials)
			return
		}
	}
}

func (c *envContextImpl) addCredential(newCredential credential.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.envStreams.AddCredential(newCredential)
	for streamProvider, handlers := range c.handlers {
		if h := streamProvider.Handler(sdkauth.NewScoped(c.filterKey, newCredential)); h != nil {
			handlers[newCredential] = h
		}
	}

	// A new SDK key means:
	//  1. we should start a new SDK client*
	//  2. we should tell all event forwarding components that use an SDK key to use the new one.
	// A new mobile key does not require starting a new SDK client, but does requiring updating any event forwarding
	// components that use a mobile key.
	// *Note: we only start a new SDK client in online mode. This is somewhat of an architectural hack because EnvContextImpl
	// is used for both offline and online mode, yet starting up an SDK client is only relevant in online mode. This is
	// because in offline mode, we already have the data (from a file) - there's no need to open a new streaming connection.
	// So, the effect in offline mode when adding/removing credentials is just setting up the new credential mappings.
	switch key := newCredential.(type) {
	case config.SDKKey:
		if !c.offline {
			go c.startSDKClient(key, nil, false)
		}
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

	c.connectionMapper.AddConnectionMapping(sdkauth.NewScoped(c.filterKey, newCredential), c)
}

func (c *envContextImpl) removeCredential(oldCredential credential.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectionMapper.RemoveConnectionMapping(sdkauth.NewScoped(c.filterKey, oldCredential))
	c.envStreams.RemoveCredential(oldCredential)
	for _, handlers := range c.handlers {
		delete(handlers, oldCredential)
	}
	// See the comment in addCredential for more context. In offline mode, there's no need to close the SDK client
	// because our data comes from a file, not a streaming connection.
	if !c.offline {
		if sdkKey, ok := oldCredential.(config.SDKKey); ok {
			// The SDK client instance is tied to the SDK key, so get rid of it
			if client := c.clients[sdkKey]; client != nil {
				delete(c.clients, sdkKey)
				_ = client.Close()
			}
		}
	}
}

func (c *envContextImpl) startSDKClient(sdkKey config.SDKKey, readyCh chan<- EnvContext, suppressErrors bool) {
	client, err := c.sdkClientFactory(sdkKey, c.sdkConfig, c.sdkInitTimeout)
	c.mu.Lock()
	name := c.identifiers.GetDisplayName()
	if client != nil {
		c.clients[sdkKey] = client

		// The data store instance is created by the SDK when it creates the client. Now that
		// we have a data store, we can finish setting up the Evaluator that we'll use for this
		// environment.
		store := c.storeAdapter.GetStore()
		dataProvider := ldstoreimpl.NewDataStoreEvaluatorDataProvider(store, c.loggers)
		evalOptions := []ldeval.EvaluatorOption{
			// We're setting EnableSecondaryKey because we may be doing evaluations for client-side SDKs that
			// are sending old-style user data with the "secondary" attribute. This option doesn't affect
			// evaluations done for newer client-side SDKs that send contexts.
			ldeval.EvaluatorOptionEnableSecondaryKey(true),
		}
		if c.sdkBigSegments != nil {
			evalOptions = append(evalOptions, ldeval.EvaluatorOptionBigSegmentProvider(c.sdkBigSegments))
		}
		c.evaluator = ldeval.NewEvaluatorWithOptions(dataProvider, evalOptions...)
	}
	c.initErr = err
	c.mu.Unlock()

	if err != nil {
		if suppressErrors {
			c.globalLoggers.Warnf("Ignoring error initializing LaunchDarkly client for %q: %+v",
				name, err)
		} else {
			c.globalLoggers.Errorf("Error initializing LaunchDarkly client for %q: %+v",
				name, err)
			if readyCh != nil {
				readyCh <- c
			}
			return
		}
	} else {
		c.globalLoggers.Infof("Initialized LaunchDarkly client for %q (SDK key %s)", name, sdkKey.Masked())
	}
	if readyCh != nil {
		readyCh <- c
	}
}

func (c *envContextImpl) GetPayloadFilter() config.FilterKey {
	return c.filterKey
}

func (c *envContextImpl) GetIdentifiers() EnvIdentifiers {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.identifiers
}

func (c *envContextImpl) SetIdentifiers(ei EnvIdentifiers) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.identifiers = ei
}

func (c *envContextImpl) UpdateCredential(update *CredentialUpdate) {
	if !update.deprecated.Defined() {
		c.keyRotator.Rotate(update.primary)
	} else {
		c.keyRotator.RotateWithGrace(update.primary, credential.NewGracePeriod(update.deprecated, update.expiry, update.now))
	}
	c.triggerCredentialChanges(update.now)
}

func (c *envContextImpl) triggerCredentialChanges(now time.Time) {
	additions, expirations := c.keyRotator.StepTime(now)
	for _, cred := range additions {
		c.addCredential(cred)
	}
	for _, cred := range expirations {
		c.removeCredential(cred)
	}
}

func (c *envContextImpl) GetCredentials() []credential.SDKCredential {
	return c.keyRotator.PrimaryCredentials()
}

func (c *envContextImpl) GetDeprecatedCredentials() []credential.SDKCredential {
	return c.keyRotator.DeprecatedCredentials()
}

func (c *envContextImpl) GetClient() sdks.LDClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// In offline mode, there's only one SDK client. This is awkward because we represent the active clients
	// as a map, but in this case there's only one client in the map. A refactoring might pull this logic (along with
	// differences in add/removeCredential into an interface that is injected based on the environment being
	// offline or online.
	if c.offline {
		for _, client := range c.clients {
			return client
		}
		return nil
	}
	return c.clients[c.keyRotator.SDKKey()]
}

func (c *envContextImpl) GetStore() subsystems.DataStore {
	return c.storeAdapter.GetStore()
}

func (c *envContextImpl) GetEvaluator() ldeval.Evaluator {
	c.mu.RLock()
	ret := c.evaluator
	c.mu.RUnlock()
	return ret
}

func (c *envContextImpl) GetBigSegmentStore() bigsegments.BigSegmentStore {
	c.mu.RLock()
	enabled := c.bigSegmentsExist
	c.mu.RUnlock()

	if enabled {
		return c.bigSegmentStore
	}
	return nil
}

func (c *envContextImpl) GetLoggers() ldlog.Loggers {
	return c.loggers
}

func (c *envContextImpl) GetStreamHandler(streamProvider streams.StreamProvider, credential credential.SDKCredential) http.Handler {
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

func (c *envContextImpl) GetFilter() config.FilterKey {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.filterKey
}

func (c *envContextImpl) GetInitError() error {
	c.mu.Lock()
	defer c.mu.Unlock()

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

func (c *envContextImpl) GetDataStoreInfo() sdks.DataStoreEnvironmentInfo {
	return c.dataStoreInfo
}

func (c *envContextImpl) GetCreationTime() time.Time {
	return c.creationTime
}

func (c *envContextImpl) FlushMetricsEvents() {
	if c.metricsEnv != nil && c.metricsEventPub != nil {
		c.metricsEnv.FlushEventsExporter()
		c.metricsEventPub.Flush()
	}
}

func (c *envContextImpl) Close() error {
	c.mu.Lock()
	for _, client := range c.clients {
		_ = client.Close()
	}
	c.clients = make(map[config.SDKKey]sdks.LDClientContext)
	c.mu.Unlock()

	close(c.stopMonitoringCredentials)
	<-c.doneMonitoringCredentials

	_ = c.envStreams.Close()

	if c.metricsManager != nil && c.metricsEnv != nil {
		c.metricsManager.RemoveEnvironment(c.metricsEnv)
	}
	if c.metricsEventPub != nil {
		c.metricsEventPub.Close()
	}
	if c.eventDispatcher != nil {
		c.eventDispatcher.Close()
	}
	if c.bigSegmentSync != nil {
		c.bigSegmentSync.Close()
	}
	if c.bigSegmentStore != nil {
		_ = c.bigSegmentStore.Close()
	}
	if c.sdkBigSegments != nil {
		c.sdkBigSegments.Close()
	}
	return nil
}

func (c *envContextImpl) setBigSegmentsExist() {
	c.mu.Lock()
	alreadyExisted := c.bigSegmentsExist
	c.bigSegmentsExist = true
	c.mu.Unlock()

	if !alreadyExisted && c.bigSegmentSync != nil {
		c.bigSegmentSync.Start()
		c.sdkBigSegments.SetPollingActive(true) // has no effect if already active
	}
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

func (u *envContextStreamUpdates) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	// We use this delegator, rather than sending updates directory to context.envStreams, so that we
	// can detect the presence of a big segment and turn on the big segment synchronizer as needed.
	u.context.envStreams.SendAllDataUpdate(allData)
	if u.context.bigSegmentSync == nil {
		return
	}

	hasBigSegment := false
	for _, coll := range allData {
		if coll.Kind == ldstoreimpl.Segments() {
			for _, keyedItem := range coll.Items {
				if s, ok := keyedItem.Item.Item.(*ldmodel.Segment); ok && s.Unbounded {
					hasBigSegment = true
					break
				}
			}
		}
	}
	if hasBigSegment {
		u.context.setBigSegmentsExist()
	}
}

func (u *envContextStreamUpdates) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	// See comments in SendAllDataUpdate.
	u.context.envStreams.SendSingleItemUpdate(kind, key, item)
	if u.context.bigSegmentSync == nil {
		return
	}
	hasBigSegment := false
	if kind == ldstoreimpl.Segments() {
		if s, ok := item.Item.(*ldmodel.Segment); ok && s.Unbounded {
			hasBigSegment = true
		}
	}
	if hasBigSegment {
		u.context.setBigSegmentsExist()
	}
}

func (u *envContextStreamUpdates) InvalidateClientSideState() {
	u.context.envStreams.InvalidateClientSideState()
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
