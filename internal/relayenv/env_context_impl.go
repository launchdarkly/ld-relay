package relayenv

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
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
	closed                    bool

	// reconcileMu serializes reconcileCredentials calls — only one runs at a time, including the
	// synchronous re-anchor sequence inside it. Held separately from mu so that GetClient / GetStore /
	// GetEvaluator / addCredential continue to run during the (potentially seconds-long) SDK client
	// construction when re-anchoring to a new key.
	reconcileMu sync.Mutex

	// anchorClientGen counts how many times the upstream anchor client has been (re)established. A
	// re-anchor commit bumps it. startSDKClient builds its client without c.mu, so a slow build can
	// finish after a later re-anchor already installed a fresh anchor client; it captures this value at
	// launch and, on completion, discards its (now stale) build if the generation has advanced rather
	// than clobbering the current anchor client. Guarded by c.mu.
	anchorClientGen uint64
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

	httpConfig, err := httpconfig.NewHTTPConfig(allConfig.Proxy, allConfig.HTTP, envConfig.SDKKey, params.UserAgent, params.Loggers)
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

		params.MetricsManager.AddEnvironmentForUsage(params.Identifiers.GetDisplayName(), envContext.metricsEventPub)
		thingsToCleanUp.AddFunc(func() { params.MetricsManager.RemoveEnvironmentForUsage(params.Identifiers.GetDisplayName()) })
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
		Events:           ldcomponents.SendEvents().EnableGzip(true),
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
	// launchGen is 0 here: no re-anchor can have committed yet (the env isn't wired into reconcile until
	// after construction returns), so this initial build is never superseded and its result is recorded.
	go envContext.startSDKClient(envConfig.SDKKey, readyCh, allConfig.Main.IgnoreConnectionErrors, 0)

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

	c.registerCredentialMappings(newCredential)

	// A new SDK key means:
	//  1. we should start a new SDK client*, but only for the anchor: there is a single upstream
	//     connection per environment, owned by the anchor key. Non-anchor server keys get their
	//     credential mappings registered above, but no upstream client — matching today's mobile-key behavior.
	//  2. we should tell all event forwarding components that use an SDK key to use the new one,
	//     again only when it is the anchor, since events collapse to the anchor per kind.
	// A new mobile key does not require starting a new SDK client, but does requiring updating any event forwarding
	// components that use a mobile key.
	// *Note: we only start a new SDK client in online mode. This is somewhat of an architectural hack because EnvContextImpl
	// is used for both offline and online mode, yet starting up an SDK client is only relevant in online mode. This is
	// because in offline mode, we already have the data (from a file) - there's no need to open a new streaming connection.
	// So, the effect in offline mode when adding/removing credentials is just setting up the new credential mappings.
	switch key := newCredential.(type) {
	case config.SDKKey:
		if key == c.keyRotator.AnchorKey() {
			if !c.offline {
				go c.startSDKClient(key, nil, false, c.anchorClientGen)
			}
			if c.metricsEventPub != nil { // metrics event publisher always uses SDK key
				c.metricsEventPub.ReplaceCredential(key)
			}
			if c.eventDispatcher != nil {
				c.eventDispatcher.ReplaceCredential(key)
			}
		}
	case config.MobileKey:
		// Mobile-key event forwarding collapses to the primary mobile key, mirroring the anchor-only
		// behavior for SDK keys above: only the primary mobile key repoints the event dispatcher, so a
		// non-primary mobile key accepted in the same reconcile does not steal event forwarding.
		if key == c.keyRotator.MobileKey() {
			if c.eventDispatcher != nil {
				c.eventDispatcher.ReplaceCredential(key)
			}
		}
	}
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

func (c *envContextImpl) startSDKClient(sdkKey config.SDKKey, readyCh chan<- EnvContext, suppressErrors bool, launchGen uint64) {
	client, err := c.sdkClientFactory(sdkKey, c.sdkConfig, c.sdkInitTimeout)
	c.mu.Lock()
	name := c.identifiers.GetDisplayName()
	// The build happens before we take c.mu. By now the env may be closed, the key may have been
	// revoked, or a re-anchor may have committed a fresh anchor client since this build was launched
	// (anchorClientGen advanced). In any of those cases this build is stale: close it rather than install
	// it, so it cannot clobber the current anchor client. This must not rely on client==nil: a failed SDK
	// build returns a non-nil, uninitialized client together with the error, so a stale failed build
	// would otherwise replace a healthy anchor client with a dead one. Only defined keys are
	// revocation-checked: an undefined SDK key is never tracked, and dropping its client would break envs
	// that legitimately run without an SDK key (offline / not-yet-configured / tests).
	superseded := c.anchorClientGen != launchGen
	droppedInactive := false
	if client != nil && (c.closed || superseded || (sdkKey.Defined() && !c.sdkKeyIsActive(sdkKey))) {
		_ = client.Close()
		client = nil
		droppedInactive = true
	}
	if client != nil {
		// If a client already exists for this key (e.g. it was re-anchored back into the anchor slot
		// while a prior client for it was still in its grace period), close the stale one before
		// replacing it so its upstream connection and goroutines are not leaked.
		if existing := c.clients[sdkKey]; existing != nil && existing != client {
			_ = existing.Close()
		}
		c.clients[sdkKey] = client
		c.rebuildEvaluator() // the SDK created the data store during Build; wire the evaluator to it now
	}
	// Record this build's result as the env's init status only when it is the current anchor's build: not
	// superseded by a newer anchor client, and its key is still the anchor. A genuine failure of the
	// current anchor is thus recorded (the middleware 401s a broken env); a stale build's late failure is
	// not, so it cannot 401 a healthy re-anchored env.
	if !superseded && sdkKey == c.keyRotator.AnchorKey() {
		c.initErr = err
	}
	c.mu.Unlock()

	switch {
	case droppedInactive:
		// The build finished but was superseded by a re-anchor, or its key was revoked, or the env
		// closed, so it was discarded above rather than installed (even if it also errored -- a
		// discarded build's error is moot). The environment is still consistent: no stale client left
		// behind.
		c.globalLoggers.Infof("SDK key %s build was superseded, revoked, or the environment was closed "+
			"before it finished initializing; the client was discarded", sdkKey.Masked())
	case err != nil:
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
	default:
		c.globalLoggers.Infof("Initialized LaunchDarkly client for %q (SDK key %s)", name, sdkKey.Masked())
	}
	if readyCh != nil {
		readyCh <- c
	}
}

// sdkKeyIsActive reports whether the given SDK key is still a tracked credential -- the anchor or a key
// within its deprecation grace period -- according to the rotator. startSDKClient uses this to avoid
// installing (and thereby leaking) a client for a key that was revoked while it was being built.
func (c *envContextImpl) sdkKeyIsActive(sdkKey config.SDKKey) bool {
	return slices.Contains(c.keyRotator.AllCredentials(), credential.SDKCredential(sdkKey))
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

func (c *envContextImpl) ReconcileCredentials(newSet credential.AcceptedSet) {
	c.reconcileCredentials(newSet, time.Now())
}

// reconcileCredentials is the time-injectable implementation of ReconcileCredentials (now is the
// reference time for expiry math).
//
// Order: add -> re-anchor -> remove. Adding first registers the new keys' mappings; the re-anchor then
// swaps the upstream client while the old anchor is still serving; removing last tears down the old
// anchor (and any revoked keys) only once the new one is up. addCredential opens an upstream client
// only for the anchor -- non-anchor server keys are routed without a second connection.
//
// reconcileMu serializes this whole method against concurrent reconciles and the cleanup ticker (see
// triggerCredentialChanges). See reanchor for the SDK-anchor swap; MobilePrimaryRepoint is handled
// inline below (a primary-mobile change to an already-accepted key isn't in additions, so addCredential
// won't repoint event forwarding for it).
func (c *envContextImpl) reconcileCredentials(newSet credential.AcceptedSet, now time.Time) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	result := c.keyRotator.Reconcile(newSet, now)
	additions, expirations := c.keyRotator.StepTime(now)

	for _, cred := range additions {
		c.addCredential(cred)
	}

	if result.AnchorChange != nil {
		if committed := c.reanchor(result.AnchorChange); !committed {
			// Rolled back: the new anchor's client never came up. Undo just this anchor change (other
			// changes in the payload stand), mirroring RevertAnchorChange. A brand-new anchor had its
			// mappings registered this cycle, so tear them down here; a previously-accepted anchor keeps
			// its mappings and reverts to the non-anchor key it already was.
			if !result.AnchorChange.NewAnchorPreviouslyAccepted {
				c.removeCredential(result.AnchorChange.NewAnchor)
			}
			c.keyRotator.RevertAnchorChange(*result.AnchorChange)
			// Keep the previous anchor's client serving by not expiring it here — even if this payload
			// revoked it outright. (A grace-demoted previous anchor isn't in expirations anyway, so this
			// only matters for an immediate revocation.)
			previousAnchor := result.AnchorChange.PreviousAnchor
			expirations = slices.DeleteFunc(expirations, func(cred credential.SDKCredential) bool {
				return cred == previousAnchor
			})
		}
	}

	if result.MobilePrimaryRepoint != nil {
		c.mu.RLock()
		dispatcher := c.eventDispatcher
		c.mu.RUnlock()
		if dispatcher != nil {
			dispatcher.ReplaceCredential(*result.MobilePrimaryRepoint)
		}
	}

	for _, cred := range expirations {
		c.removeCredential(cred)
	}
}

// reanchor drives the synchronous re-anchor sequence for an SDK anchor change signaled by Reconcile's
// ReconcileResult.AnchorChange. Invoked by reconcileCredentials after additions have been processed
// and before expirations, so the previous anchor's client is still alive while the new client is built
// (or reused).
//
// reanchor holds c.mu for the whole sequence and releases it only around the SDK client build (which
// must not hold the lock — see buildNewAnchorClient). Holding one continuous lock otherwise keeps
// Close() (which also takes c.mu) from tearing down clients or the dispatcher mid-commit, and lets
// commitReanchor assume the lock is held rather than re-acquiring it.
//
//   - When there is no existing client for the new anchor and the env is online: register its credential
//     mappings if the key is brand new (Reconcile stripped it from additions), build a new SDK client,
//     and on Initialized commit the anchor. On init failure, roll back: do not commit, leave the previous
//     anchor authoritative (its client keeps serving), and log a structured error.
//   - When a client already exists (e.g. a former anchor still in its grace period), or the env is
//     offline: no build, just commit.
//
// Returns true if the anchor was committed, false if it rolled back (init failure or the env closed
// mid-build), so reconcileCredentials can back out the anchor change. The old anchor's client is not
// closed here; its grace-period expiration drives removeCredential.
func (c *envContextImpl) reanchor(change *credential.AnchorChange) bool {
	newAnchor := change.NewAnchor
	previousAnchor := change.PreviousAnchor

	c.mu.Lock()
	defer c.mu.Unlock()

	// Two independent questions:
	//   - NewAnchorPreviouslyAccepted: are this key's credential mappings already registered? A brand-new
	//     anchor was stripped from additions, so register them now; an already-accepted key already has them.
	//   - the client check below: does a client already exist? If so reuse it, else build one.
	// They differ for a previously-accepted non-anchor key promoted to anchor: mappings exist, client
	// does not. (A live client always implies the key was already accepted, so registration never double-fires.)
	if !change.NewAnchorPreviouslyAccepted {
		c.registerCredentialMappings(newAnchor)
	}

	// A live client for the new anchor is reused as-is; an offline env has no upstream client to build.
	// Either way there is nothing to build, so fall through to commit.
	why := "reused existing client"
	if c.clients[newAnchor] == nil {
		if c.offline {
			why = "offline — no client build"
		} else {
			// Build the new client without the lock: sdkClientFactory can block for up to sdkInitTimeout,
			// and holding c.mu that long would stall every GetClient/GetStore caller (see reconcileMu).
			// reanchor's deferred Unlock releases the lock we re-acquire here on return.
			c.mu.Unlock()
			client := c.buildNewAnchorClient(newAnchor, previousAnchor)
			c.mu.Lock()

			if client == nil {
				// Init failed; buildNewAnchorClient already logged and closed the half-built client. Do not
				// commit — leave the previous anchor authoritative.
				return false
			}
			if c.closed {
				// The env was torn down while the lock was released for the build (Close() does not hold
				// reconcileMu, so it can run concurrently); its client-teardown loop has already finished and
				// would never close this one, so discard the freshly-built client rather than install it into
				// a closed env (mirrors the guard in startSDKClient).
				_ = client.Close()
				return false
			}
			if existing := c.clients[newAnchor]; existing != nil && existing != client {
				// Stale-client guard: the lock was released for the build, so re-check and close any client
				// installed concurrently for newAnchor.
				_ = existing.Close()
			}
			c.clients[newAnchor] = client
			// With store handover, GetStore() returns the SAME wrapper the old client used, so the rebuilt
			// evaluator serves the already-populated data immediately (no empty-store window).
			c.rebuildEvaluator()
			why = "built new client"
		}
	}

	return c.commitReanchor(newAnchor, previousAnchor, why)
}

// buildNewAnchorClient constructs the SDK client for a re-anchor to newAnchor. It must run without c.mu
// held: sdkClientFactory can block for up to sdkInitTimeout, and holding the lock that long would stall
// every GetClient/GetStore caller. It touches only fields fixed at construction (sdkClientFactory,
// sdkConfig, sdkInitTimeout, globalLoggers), so it needs no lock — mirroring startSDKClient, which also
// builds before locking.
//
// Returns the initialized client, or nil if the build failed, in which case it has already closed any
// half-built client and logged a structured error. initErr is deliberately left untouched on failure:
// it feeds the request middleware, and setting it to the new anchor's ErrInitializationFailed would 401
// an env that is serving fine on the previous anchor.
func (c *envContextImpl) buildNewAnchorClient(newAnchor, previousAnchor config.SDKKey) sdks.LDClientContext {
	client, err := c.sdkClientFactory(newAnchor, c.sdkConfig, c.sdkInitTimeout)
	if err != nil || client == nil || !client.Initialized() {
		var initialized bool
		if client != nil {
			initialized = client.Initialized()
			_ = client.Close()
		}
		c.globalLoggers.Errorf("Re-anchor to SDK key %s failed (err=%v initialized=%v); "+
			"preserving previous anchor %s",
			newAnchor.Masked(), err, initialized, previousAnchor.Masked())
		return nil
	}
	return client
}

// commitReanchor is the second half of the re-anchor sequence: atomically move the rotator's anchor
// pointer, clear any stale init error now that a healthy client is current, and repoint downstream
// event/metrics forwarding. The caller must hold c.mu — reanchor holds it across the whole sequence, so
// the commit and Close() (which also takes c.mu) are mutually exclusive and Close can't tear the client
// or dispatcher out mid-commit. This mirrors addCredential, which likewise repoints event forwarding
// and reads rotator state under c.mu.
//
// Returns false without committing if the env was closed first, so callers report the rollback rather
// than a phantom success.
func (c *envContextImpl) commitReanchor(newAnchor, previousAnchor config.SDKKey, why string) bool {
	if c.closed {
		// Close() ran before we could commit. Don't flip the anchor or touch the (now-closed) dispatcher
		// and metrics publisher; the env is being torn down.
		return false
	}

	c.keyRotator.CommitAnchor(newAnchor)
	// A new anchor client is now authoritative, so any startSDKClient build still in flight from before
	// this commit is stale: bump the generation so it discards itself instead of clobbering this client.
	c.anchorClientGen++
	// The anchor now points at a healthy client (freshly built and Initialized, or a reused live
	// client), so clear any init error a prior client left behind — otherwise GetInitError() and the
	// request middleware would keep reporting a still-serving env as failed.
	c.initErr = nil

	if c.metricsEventPub != nil {
		c.metricsEventPub.ReplaceCredential(newAnchor)
	}
	if c.eventDispatcher != nil {
		c.eventDispatcher.ReplaceCredential(newAnchor)
	}

	// Big-segment synchronization is intentionally left pointing at the previous anchor key across a
	// re-anchor: this matches pre-concurrent-keys behavior (there was no re-anchor, so it never moved)
	// and does not regress. When big-segment re-anchor is implemented, its re-wire hook belongs right
	// here, after the event/metrics ReplaceCredential calls — either recreate the BigSegmentSynchronizer
	// for newAnchor, or add a credential-replacement method to it.

	c.globalLoggers.Infof("Re-anchored SDK from %s to %s (%s)", previousAnchor.Masked(), newAnchor.Masked(), why)
	return true
}

// rebuildEvaluator constructs the environment's Evaluator against the current data store. It is called
// after (re)creating an SDK client, once the store is available, and is shared by the initial client
// startup and the re-anchor path. It reads and writes envContextImpl fields directly, so the caller
// must hold c.mu.
//
// EnableSecondaryKey is set because we may evaluate for client-side SDKs sending old-style user data
// with the "secondary" attribute; it has no effect for newer SDKs that send contexts.
func (c *envContextImpl) rebuildEvaluator() {
	store := c.storeAdapter.GetStore()
	dataProvider := ldstoreimpl.NewDataStoreEvaluatorDataProvider(store, c.loggers)
	evalOptions := []ldeval.EvaluatorOption{
		ldeval.EvaluatorOptionEnableSecondaryKey(true),
	}
	if c.sdkBigSegments != nil {
		evalOptions = append(evalOptions, ldeval.EvaluatorOptionBigSegmentProvider(c.sdkBigSegments))
	}
	c.evaluator = ldeval.NewEvaluatorWithOptions(dataProvider, evalOptions...)
}

// registerCredentialMappings wires relay's downstream-facing routing for cred: it registers the
// credential with the env's stream machinery, builds the per-stream-provider HTTP handlers, and adds
// the connection→env mapping, so incoming SDK/client connections that authenticate with cred are
// served by this env. It does NOT start the upstream SDK client or repoint event/metrics forwarding —
// those are anchor-only concerns owned by the callers (addCredential, and the re-anchor sequence).
// The caller must hold c.mu.
func (c *envContextImpl) registerCredentialMappings(cred credential.SDKCredential) {
	c.envStreams.AddCredential(cred)
	for streamProvider, handlers := range c.handlers {
		if h := streamProvider.Handler(sdkauth.NewScoped(c.filterKey, cred)); h != nil {
			handlers[cred] = h
		}
	}
	c.connectionMapper.AddConnectionMapping(sdkauth.NewScoped(c.filterKey, cred), c)
}

// triggerCredentialChanges drains the rotator's StepTime queue and applies the resulting additions
// and expirations. It runs on the cleanup ticker (cleanupExpiredCredentials), so it can fire at any
// moment — including while a synchronous re-anchor is in flight inside reconcileCredentials.
//
// It takes reconcileMu for the whole StepTime + add/remove pass so the ticker is serialized against
// reconcileCredentials exactly the way concurrent reconciles already are. Without it, a credential
// expiry firing during an in-flight re-anchor would drain the same StepTime queue the reconcile
// relies on (the ticker could steal additions a reconcile just queued) and could removeCredential —
// closing a client — partway through the re-anchor sequence. reconcileCredentials never calls this,
// so taking reconcileMu here introduces no re-entrancy.
func (c *envContextImpl) triggerCredentialChanges(now time.Time) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	additions, expirations := c.keyRotator.StepTime(now)
	for _, cred := range additions {
		c.addCredential(cred)
	}
	for _, cred := range expirations {
		c.removeCredential(cred)
	}
}

func (c *envContextImpl) GetCredentials() []credential.SDKCredential {
	return c.keyRotator.AllCredentials()
}

func (c *envContextImpl) GetAnchorKey() config.SDKKey {
	return c.keyRotator.AnchorKey()
}

func (c *envContextImpl) GetMobileKey() config.MobileKey {
	return c.keyRotator.MobileKey()
}

func (c *envContextImpl) GetDeprecatedCredentials() []credential.SDKCredential {
	return c.keyRotator.DeprecatedCredentials()
}

func (c *envContextImpl) GetAcceptedKeys() credential.AcceptedKeySet {
	return c.keyRotator.AcceptedKeys()
}

func (c *envContextImpl) GetClient() sdks.LDClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// c.clients always has at most one entry — the anchor's client. Only the anchor key triggers
	// startSDKClient (in addCredential and at construction), so non-anchor server keys never open
	// their own upstream connection.
	//
	// Offline mode uses iteration rather than key-based lookup for historical reasons; both
	// approaches are correct because keyRotator is initialized with envConfig.SDKKey before
	// startSDKClient is ever called.
	if c.offline {
		for _, client := range c.clients {
			return client
		}
		return nil
	}
	return c.clients[c.keyRotator.AnchorKey()]
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

func (c *envContextImpl) GetMetricsManager() *metrics.Manager {
	return c.metricsManager
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
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
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
	if c.metricsManager != nil {
		c.metricsManager.RemoveEnvironmentForUsage(c.identifiers.GetDisplayName())
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
