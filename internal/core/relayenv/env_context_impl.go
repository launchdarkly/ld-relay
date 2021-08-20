package relayenv

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/bigsegments"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/core/streams"
	"github.com/launchdarkly/ld-relay/v6/internal/util"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldeval "gopkg.in/launchdarkly/go-server-sdk-evaluation.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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

// EnvContextImplParams contains the constructor parameters for NewEnvContextImpl. These have their
// own type because there are a lot of them, and many are irrelevant in tests.
type EnvContextImplParams struct {
	Identifiers                   EnvIdentifiers
	EnvConfig                     config.EnvConfig
	AllConfig                     config.Config
	ClientFactory                 sdks.ClientFactoryFunc
	DataStoreFactory              interfaces.DataStoreFactory
	DataStoreInfo                 sdks.DataStoreEnvironmentInfo
	StreamProviders               []streams.StreamProvider
	JSClientContext               JSClientContext
	MetricsManager                *metrics.Manager
	BigSegmentStoreFactory        bigsegments.BigSegmentStoreFactory
	BigSegmentSynchronizerFactory bigsegments.BigSegmentSynchronizerFactory
	SDKBigSegmentsConfigFactory   interfaces.BigSegmentsConfigurationFactory // set only in tests
	UserAgent                     string
	LogNameMode                   LogNameMode
	Loggers                       ldlog.Loggers
}

type envContextImpl struct {
	mu               sync.RWMutex
	clients          map[config.SDKKey]sdks.LDClientContext
	storeAdapter     *store.SSERelayDataStoreAdapter
	loggers          ldlog.Loggers
	credentials      map[config.SDKCredential]bool // true if not deprecated
	identifiers      EnvIdentifiers
	secureMode       bool
	envStreams       *streams.EnvStreams
	streamProviders  []streams.StreamProvider
	handlers         map[streams.StreamProvider]map[config.SDKCredential]http.Handler
	jsContext        JSClientContext
	evaluator        ldeval.Evaluator
	eventDispatcher  *events.EventDispatcher
	bigSegmentSync   bigsegments.BigSegmentSynchronizer
	bigSegmentStore  bigsegments.BigSegmentStore
	sdkBigSegments   *ldstoreimpl.BigSegmentStoreWrapper
	sdkConfig        ld.Config
	sdkClientFactory sdks.ClientFactoryFunc
	sdkInitTimeout   time.Duration
	metricsManager   *metrics.Manager
	metricsEnv       *metrics.EnvironmentManager
	metricsEventPub  events.EventPublisher
	dataStoreInfo    sdks.DataStoreEnvironmentInfo
	globalLoggers    ldlog.Loggers
	ttl              time.Duration
	initErr          error
	creationTime     time.Time
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
func NewEnvContext( //nolint:gocyclo
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

	credentials := make(map[config.SDKCredential]bool, 3)
	credentials[envConfig.SDKKey] = true
	if envConfig.MobileKey != "" {
		credentials[envConfig.MobileKey] = true
	}
	if envConfig.EnvID != "" {
		credentials[envConfig.EnvID] = true
	}

	envContext := &envContextImpl{
		identifiers:      params.Identifiers,
		clients:          make(map[config.SDKKey]sdks.LDClientContext),
		credentials:      credentials,
		loggers:          envLoggers,
		secureMode:       envConfig.SecureMode,
		streamProviders:  params.StreamProviders,
		handlers:         make(map[streams.StreamProvider]map[config.SDKCredential]http.Handler),
		jsContext:        params.JSClientContext,
		sdkClientFactory: params.ClientFactory,
		sdkInitTimeout:   allConfig.Main.InitTimeout.GetOrElse(config.DefaultInitTimeout),
		metricsManager:   params.MetricsManager,
		globalLoggers:    params.Loggers,
		ttl:              envConfig.TTL.GetOrElse(0),
		dataStoreInfo:    params.DataStoreInfo,
		creationTime:     time.Now(),
	}

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
		envLoggers,
	)
	envContext.envStreams = envStreams
	thingsToCleanUp.AddCloser(envStreams)

	envStreamUpdates := &envContextStreamUpdates{
		context: envContext,
	}

	for c := range credentials {
		envStreams.AddCredential(c)
	}
	for _, sp := range params.StreamProviders {
		handlers := make(map[config.SDKCredential]http.Handler)
		for c := range credentials {
			h := sp.Handler(c)
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
			eventDispatcher = events.NewEventDispatcher(envConfig.SDKKey, envConfig.MobileKey, envConfig.EnvID,
				envLoggers, allConfig.Events, httpConfig, storeAdapter)
		}
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

	enableDiagnostics := !allConfig.Main.DisableInternalUsageMetrics && !offlineMode
	var em *metrics.EnvironmentManager
	if params.MetricsManager != nil {
		if enableDiagnostics {
			pubLoggers := envLoggers
			pubLoggers.SetPrefix(logPrefix + " (usage metrics)")
			eventsPublisher, err := events.NewHTTPEventPublisher(envConfig.SDKKey, httpConfig, pubLoggers,
				events.OptionURI(eventsURI))
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

	envContext.sdkConfig = ld.Config{
		DataSource:       ldcomponents.StreamingDataSource().BaseURI(streamURI),
		DataStore:        storeAdapter,
		DiagnosticOptOut: !enableDiagnostics,
		Events:           ldcomponents.SendEvents().BaseURI(eventsURI),
		HTTP:             httpConfig.SDKHTTPConfigFactory,
		Logging: ldcomponents.Logging().
			Loggers(envLoggers).
			LogDataSourceOutageAsErrorAfter(disconnectedStatusTime),
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
		bigSegConfig, err := configFactory.CreateBigSegmentsConfiguration(
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
					UserCacheSize:      bigSegConfig.GetUserCacheSize(),
					UserCacheTime:      bigSegConfig.GetUserCacheTime(),
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

	thingsToCleanUp.Clear() // we've succeeded so we do not want to throw away these things

	return envContext, nil
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
		if c.sdkBigSegments == nil {
			c.evaluator = ldeval.NewEvaluator(dataProvider)
		} else {
			c.evaluator = ldeval.NewEvaluatorWithBigSegments(dataProvider, c.sdkBigSegments)
		}
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
		c.globalLoggers.Infof("Initialized LaunchDarkly client for %q", name)
	}
	if readyCh != nil {
		readyCh <- c
	}
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

func (c *envContextImpl) GetCredentials() []config.SDKCredential {
	return c.getCredentialsInternal(true)
}

func (c *envContextImpl) GetDeprecatedCredentials() []config.SDKCredential {
	return c.getCredentialsInternal(false)
}

func (c *envContextImpl) getCredentialsInternal(preferred bool) []config.SDKCredential {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ret := make([]config.SDKCredential, 0, len(c.credentials))
	for c, nonDeprecated := range c.credentials {
		if nonDeprecated == preferred {
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
	for streamProvider, handlers := range c.handlers {
		if h := streamProvider.Handler(newCredential); h != nil {
			handlers[newCredential] = h
		}
	}

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
		for _, handlers := range c.handlers {
			delete(handlers, oldCredential)
		}
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

func (c *envContextImpl) GetEvaluator() ldeval.Evaluator {
	c.mu.RLock()
	ret := c.evaluator
	c.mu.RUnlock()
	return ret
}

func (c *envContextImpl) GetBigSegmentStore() bigsegments.BigSegmentStore {
	return c.bigSegmentStore
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
		u.context.bigSegmentSync.Start() // has no effect if already started
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
		u.context.bigSegmentSync.Start()                // has no effect if already started
		u.context.sdkBigSegments.SetPollingActive(true) // has no effect if already active
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
