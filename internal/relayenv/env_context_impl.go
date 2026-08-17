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
	mu              sync.RWMutex
	clients         map[config.SDKKey]sdks.LDClientContext
	storeAdapter    *store.SSERelayDataStoreAdapter
	loggers         ldlog.Loggers
	identifiers     EnvIdentifiers
	secureMode      bool
	envStreams      *streams.EnvStreams
	streamProviders []streams.StreamProvider
	jsContext       JSClientContext
	evaluator       ldeval.Evaluator
	eventDispatcher *events.EventDispatcher
	bigSegmentSync  bigsegments.BigSegmentSynchronizer
	// makeBigSegmentSync builds a BigSegmentSynchronizer for an anchor SDK key. A re-anchor uses it
	// to rebuild the synchronizer on the new anchor. nil when big segments are not configured.
	makeBigSegmentSync        func(anchor config.SDKKey) bigsegments.BigSegmentSynchronizer
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

	// reconcileSem serializes reconcileCredentials calls, including the re-anchor sequence they run. It
	// is held separately from mu so that readers keep running during the SDK client construction.
	//
	// It is a channel with capacity 1 rather than a mutex because the cleanup ticker must be able to
	// abandon its wait, and you cannot select on a mutex. See acquireReconcileUnlessStopping.
	reconcileSem chan struct{}

	// anchorClientGen counts how many times the anchor client has been established. A re-anchor
	// commit bumps it. startSDKClient captures it at launch and discards its build if the value
	// advanced, because a slow build can finish after a later re-anchor. Guarded by c.mu.
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
		reconcileSem:              make(chan struct{}, 1),
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
		// Bind the construction-time inputs so a re-anchor can rebuild the synchronizer on the new
		// anchor key (see reanchorBigSegmentSync). The synchronizer authenticates with the key it gets.
		baseURI := allConfig.Main.BaseURI.String()
		streamURI := allConfig.Main.StreamURI.String()
		envContext.makeBigSegmentSync = func(anchor config.SDKKey) bigsegments.BigSegmentSynchronizer {
			return factory(httpConfig, bigSegmentStore, baseURI, streamURI, envConfig.EnvID, anchor, envLoggers, logPrefix)
		}
		envContext.bigSegmentSync = envContext.makeBigSegmentSync(envConfig.SDKKey)
		thingsToCleanUp.AddFunc(envContext.bigSegmentSync.Close)
		envContext.consumeBigSegmentUpdates(envContext.bigSegmentSync)
		// We deliberately do not call bigSegmentSync.Start() here because we don't want the synchronizer
		// to start until we know that at least one big segment exists. That's implemented by the
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
				events.OptionBaseURI(eventsURI),
				events.OptionCapacity(allConfig.Events.MetricsCapacity.GetOrElse(config.DefaultMetricsCapacity)),
				events.OptionInitialCapacity(config.DefaultMetricsInitialCapacity))
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
	// launchGen is 0 here: no re-anchor can have committed yet, so this build is never superseded.
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

	// Event forwarding collapses to the primary mobile key, so only the primary repoints the dispatcher.
	// Client lifecycle and SDK-key event forwarding belong to NewEnvContext and commitReanchor.
	if mobileKey, ok := newCredential.(config.MobileKey); ok && mobileKey == c.keyRotator.MobileKey() {
		if c.eventDispatcher != nil {
			c.eventDispatcher.ReplaceCredential(mobileKey)
		}
	}
}

func (c *envContextImpl) removeCredential(oldCredential credential.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectionMapper.RemoveConnectionMapping(sdkauth.NewScoped(c.filterKey, oldCredential))
	c.envStreams.RemoveCredential(oldCredential)
	// In offline mode, there's no need to close the SDK client because our data comes from a file,
	// not a streaming connection.
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
	// The build ran without c.mu, so it may now be stale: the env closed, the key was revoked, or a
	// re-anchor advanced anchorClientGen. Close a stale build instead of installing it.
	//
	// The guard cannot test client == nil, because a failed build returns a non-nil uninitialized
	// client. Only defined keys are revocation-checked: an undefined key is never tracked, and envs
	// legitimately run without one.
	superseded := c.anchorClientGen != launchGen
	droppedInactive := false
	if client != nil && (c.closed || superseded || (sdkKey.Defined() && !c.sdkKeyIsActive(sdkKey))) {
		_ = client.Close()
		client = nil
		droppedInactive = true
	}
	if client != nil {
		// Close any stale client for this key, so its connection and goroutines are not leaked.
		if existing := c.clients[sdkKey]; existing != nil && existing != client {
			_ = existing.Close()
		}
		c.clients[sdkKey] = client
		c.rebuildEvaluator() // the SDK created the data store during Build; wire the evaluator to it now
	}
	// Record this build's result as the env's init status only for the current anchor's build. A stale
	// build's late failure must not 401 a healthy re-anchored env.
	if !superseded && sdkKey == c.keyRotator.AnchorKey() {
		c.initErr = err
	}
	c.mu.Unlock()

	switch {
	case droppedInactive:
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

// sdkKeyIsActive reports whether the rotator still accepts sdkKey. startSDKClient uses this to avoid
// installing a client for a key revoked while the client was building.
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

// acquireReconcile blocks until this goroutine holds reconcileSem. releaseReconcile hands it back.
// Together they are the mutex that reconcileSem stands in for.
func (c *envContextImpl) acquireReconcile() {
	c.reconcileSem <- struct{}{}
}

func (c *envContextImpl) releaseReconcile() {
	<-c.reconcileSem
}

// acquireReconcileUnlessStopping is acquireReconcile for the cleanup ticker, which cannot wait
// unconditionally. reconcileSem is held across the re-anchor's client build, which blocks for up to
// sdkInitTimeout. Close() waits for the ticker goroutine to observe stopMonitoringCredentials, so an
// unconditional wait pins Close() behind the whole build.
//
// It returns true when it acquires reconcileSem, and the caller must then call releaseReconcile. It
// returns false when the env is shutting down. Only the wait is abandoned: a ticker that does acquire
// still runs its pass exclusively against reconcileCredentials. A skipped pass costs nothing, because
// it would only expire credentials in an env that is closing.
func (c *envContextImpl) acquireReconcileUnlessStopping() bool {
	select {
	case c.reconcileSem <- struct{}{}:
		return true
	case <-c.stopMonitoringCredentials:
		return false
	}
}

func (c *envContextImpl) ReconcileCredentials(newSet credential.AcceptedSet) {
	c.reconcileCredentials(newSet, time.Now())
}

// reconcileCredentials is the time-injectable implementation of ReconcileCredentials. now is the
// reference time for expiry math.
//
// The order is add, re-anchor, remove. Adding first registers the new keys' mappings. The re-anchor
// then swaps the upstream client while the old anchor still serves. Removing last tears down revoked
// mappings only after the new anchor is up. addCredential never opens a client; only reanchor does.
//
// reconcileSem serializes this method against concurrent reconciles and the cleanup ticker.
func (c *envContextImpl) reconcileCredentials(newSet credential.AcceptedSet, now time.Time) {
	c.acquireReconcile()
	defer c.releaseReconcile()

	result := c.keyRotator.Reconcile(newSet, now)
	additions, expirations := c.keyRotator.StepTime(now)

	for _, cred := range additions {
		c.addCredential(cred)
	}

	if result.AnchorChange != nil {
		if committed := c.reanchor(result.AnchorChange); !committed {
			// Rolled back: the new anchor's client never came up. Undo only the anchor change; the other
			// changes in this payload stand. A brand-new anchor had its mappings registered this cycle,
			// so tear them down here.
			if !result.AnchorChange.NewAnchorPreviouslyAccepted {
				c.removeCredential(result.AnchorChange.NewAnchor)
			}
			c.keyRotator.RevertAnchorChange(*result.AnchorChange)
			// Keep the previous anchor's client serving by not expiring that key here, even if this
			// payload revoked it outright.
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

// reanchor moves the environment's upstream connection to change.NewAnchor. It returns true if the
// anchor was committed, and false if it rolled back (the build failed, or the env closed).
// This function is the canonical re-anchor sequence: Reconcile signals the anchor change but does not
// flip the rotator's pointer; reanchor builds or reuses the client, then commitReanchor moves it.
//
// Concurrency: reanchor holds c.mu for the whole sequence and releases it only around the client
// build. The continuous lock keeps Close() out of the middle of a commit, and lets commitReanchor
// assume the lock is held.
//
// The new anchor needs a client only when none exists and the env is online.
//
// On commit, reanchor closes the previous anchor's client. Two clients would feed the same store
// wrapper and broadcast every update twice. The demoted key keeps its credential mappings, so it
// still authenticates downstream connections until its grace period expires.
func (c *envContextImpl) reanchor(change *credential.AnchorChange) bool {
	newAnchor := change.NewAnchor
	previousAnchor := change.PreviousAnchor

	c.mu.Lock()
	defer c.mu.Unlock()

	// Mappings can exist without a client, but never the reverse. Reconcile stripped a brand-new anchor
	// from additions, so register its mappings here; an already-accepted key has them already.
	if !change.NewAnchorPreviouslyAccepted {
		c.registerCredentialMappings(newAnchor)
	}

	// A live client is reused as-is, and an offline env builds none. Both cases fall through to commit.
	why := "reused existing client"
	if c.clients[newAnchor] == nil {
		if c.offline {
			why = "offline — no client build"
		} else {
			// Build without the lock: sdkClientFactory can block for up to sdkInitTimeout, and holding
			// c.mu that long would stall every GetClient and GetStore caller.
			c.mu.Unlock()
			client := c.buildNewAnchorClient(newAnchor, previousAnchor)
			c.mu.Lock()

			if client == nil {
				// Init failed. buildNewAnchorClient logged the error and closed the half-built client.
				return false
			}
			if c.closed {
				// Close() ran while the lock was released, and its client-teardown loop already finished,
				// so it would never close this client. Discard the client instead of installing it.
				_ = client.Close()
				return false
			}
			if existing := c.clients[newAnchor]; existing != nil && existing != client {
				// The lock was released for the build, so close any client installed concurrently.
				_ = existing.Close()
			}
			c.clients[newAnchor] = client
			// GetStore() returns the same wrapper the old client used, so there is no empty-store window.
			c.rebuildEvaluator()
			why = "built new client"
		}
	}

	if !c.commitReanchor(newAnchor, previousAnchor, why) {
		return false
	}

	// The new anchor's client is now authoritative, so close the previous anchor's client. The shared
	// store wrapper survives: it is refcounted and the new anchor's client holds it. Offline mode is
	// exempt because it built no replacement, and its single file-data client must keep serving.
	if !c.offline {
		if oldClient := c.clients[previousAnchor]; oldClient != nil {
			delete(c.clients, previousAnchor)
			_ = oldClient.Close()
		}
	}
	return true
}

// buildNewAnchorClient constructs the SDK client for a re-anchor to newAnchor. It returns nil if the
// build failed, after closing any half-built client and logging the error.
//
// The caller must not hold c.mu: sdkClientFactory can block for sdkInitTimeout, which would stall
// every GetClient and GetStore caller. This method reads only construction-time fields.
//
// It leaves initErr alone on failure. initErr feeds the request middleware, so setting it would 401
// an env that is still serving on the previous anchor.
//
// The Close() below relies on the store-release contract in sdks.ClientFactoryFunc.
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

// commitReanchor is the second half of the re-anchor sequence: move the rotator's anchor pointer,
// clear a stale init error, and repoint event and metrics forwarding. It returns false without
// committing if the env was closed first. The caller must hold c.mu.
func (c *envContextImpl) commitReanchor(newAnchor, previousAnchor config.SDKKey, why string) bool {
	if c.closed {
		// Close() ran first. The env is being torn down, so do not flip the anchor.
		return false
	}

	c.keyRotator.CommitAnchor(newAnchor)
	// Any startSDKClient build still in flight is now stale, so bump the generation to make that build
	// discard itself. Bump only when online: an offline commit installs no replacement client, and the
	// bump would strand the env's initial build, leaving GetClient() nil forever.
	if !c.offline {
		c.anchorClientGen++
	}
	// The anchor now points at a healthy client, so clear any init error a prior client left behind.
	c.initErr = nil

	if c.metricsEventPub != nil {
		c.metricsEventPub.ReplaceCredential(newAnchor)
	}
	if c.eventDispatcher != nil {
		c.eventDispatcher.ReplaceCredential(newAnchor)
	}

	// Big-segment requests authenticate with the anchor SDK key, so the synchronizer follows the anchor.
	c.reanchorBigSegmentSync(newAnchor)

	c.globalLoggers.Infof("Re-anchored SDK from %s to %s (%s)", previousAnchor.Masked(), newAnchor.Masked(), why)
	return true
}

// rebuildEvaluator constructs the environment's Evaluator against the current data store. Call it
// after creating an SDK client. The caller must hold c.mu.
//
// EnableSecondaryKey supports client-side SDKs that send old-style user data with the "secondary"
// attribute. It has no effect for SDKs that send contexts.
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

// registerCredentialMappings registers cred with the env's stream machinery and adds the
// connection-to-env mapping, so connections that authenticate with cred reach this env. Stream
// handlers are built per request in GetStreamHandler. The caller must hold c.mu.
func (c *envContextImpl) registerCredentialMappings(cred credential.SDKCredential) {
	c.envStreams.AddCredential(cred)
	c.connectionMapper.AddConnectionMapping(sdkauth.NewScoped(c.filterKey, cred), c)
}

// triggerCredentialChanges drains the rotator's StepTime queue and applies the additions and
// expirations. It runs on the cleanup ticker, so it can fire during an in-flight re-anchor.
//
// It holds reconcileSem for the whole pass. Without that lock the ticker could steal additions a
// reconcile just queued, or close a client partway through a re-anchor. reconcileCredentials never
// calls this function, so there is no re-entrancy.
//
// It abandons the acquisition when the env shuts down and returns without a pass, because Close()
// waits for this goroutine to exit. See acquireReconcileUnlessStopping.
func (c *envContextImpl) triggerCredentialChanges(now time.Time) {
	if !c.acquireReconcileUnlessStopping() {
		return
	}
	defer c.releaseReconcile()

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
	// c.clients holds at most one entry: the anchor's client. Only construction and the re-anchor
	// sequence create clients, so non-anchor server keys never open an upstream connection. Offline mode
	// iterates instead of looking up by key. Both work: the rotator is initialized with envConfig.SDKKey
	// before any client is built.
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

func (c *envContextImpl) GetStreamHandler(streamProvider streams.StreamProvider, cred credential.SDKCredential) http.Handler {
	// Build the handler on demand: every handler in a (filter, provider) slot differs only by the
	// credential-derived channel id. c.filterKey is immutable after construction, so this needs no lock.
	if h := streamProvider.Handler(sdkauth.NewScoped(c.filterKey, cred)); h != nil {
		return h
	}
	return http.HandlerFunc(invalidStreamHandler)
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
	c.mu.RLock()
	defer c.mu.RUnlock()

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

	// Stop the cleanup ticker and wait for its goroutine to exit. Its pass touches envStreams, the
	// dispatcher, and metrics, which the teardown below closes. The wait lasts as long as the ticker's
	// slowest path to the stop signal, so nothing on that path can block indefinitely. See
	// acquireReconcileUnlessStopping.
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

// consumeBigSegmentUpdates spawns a goroutine that drains sync's update channel and broadcasts a
// cache-clear and a client-side invalidation for each batch. The goroutine exits when sync closes.
func (c *envContextImpl) consumeBigSegmentUpdates(sync bigsegments.BigSegmentSynchronizer) {
	ch := sync.SegmentUpdatesCh()
	if ch == nil {
		return
	}
	go func() {
		for range ch {
			// The batch's segment keys are not needed: relay pings all connected client-side SDKs.
			if c.sdkBigSegments != nil {
				c.sdkBigSegments.ClearCache()
			}
			if c.envStreams != nil {
				c.envStreams.InvalidateClientSideState()
			}
		}
	}()
}

// reanchorBigSegmentSync rebuilds the big-segment synchronizer on the new anchor. The synchronizer
// bakes in its SDK key and is not restartable, so a re-anchor recreates it. It starts the replacement
// when the old synchronizer was started, then closes the old one. The caller holds c.mu.
func (c *envContextImpl) reanchorBigSegmentSync(newAnchor config.SDKKey) {
	if c.bigSegmentSync == nil {
		return
	}
	wasStarted := c.bigSegmentsExist
	old := c.bigSegmentSync
	c.bigSegmentSync = c.makeBigSegmentSync(newAnchor)
	c.consumeBigSegmentUpdates(c.bigSegmentSync)
	if wasStarted {
		c.bigSegmentSync.Start()
	}
	old.Close()
}

func (c *envContextImpl) setBigSegmentsExist() {
	c.mu.Lock()
	firstTime := !c.bigSegmentsExist
	c.bigSegmentsExist = true
	// Start c.bigSegmentSync while holding the lock. Starting it after unlocking could start a
	// synchronizer that a concurrent re-anchor already retired. Start() only launches a goroutine, so
	// holding c.mu is safe.
	started := firstTime && c.bigSegmentSync != nil
	if started {
		c.bigSegmentSync.Start()
	}
	c.mu.Unlock()

	if started {
		c.sdkBigSegments.SetPollingActive(true) // has no effect if already active
	}
}

// bigSegmentSyncConfigured reports whether this env has a big-segment synchronizer. The read is
// synchronized against the reassign in reanchorBigSegmentSync: the store-update sink below runs on
// the SDK data-source goroutine while a re-anchor runs on the reconcile goroutine.
func (c *envContextImpl) bigSegmentSyncConfigured() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bigSegmentSync != nil
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
	if !u.context.bigSegmentSyncConfigured() {
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
	if !u.context.bigSegmentSyncConfigured() {
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
