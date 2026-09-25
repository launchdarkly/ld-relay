package relayenv

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/datadestination"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"

	"github.com/launchdarkly/ld-relay/v9/internal/credential"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	ldeval "github.com/launchdarkly/go-server-sdk-evaluation/v3"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	"golang.org/x/sync/singleflight"
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
	AddConnectionMapping(scopedCredential credential.SDKCredential, envContext EnvContext)
	RemoveConnectionMapping(scopedCredential credential.SDKCredential)
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
	Logger                           *slog.Logger
	ConnectionMapper                 ConnectionMapper
	ExpiredCredentialCleanupInterval time.Duration
}

type envContextImpl struct {
	mu sync.RWMutex
	// client is the environment's single SDK client. Only construction creates one: a key rotation
	// re-keys this client in place rather than building a replacement, so an environment never holds
	// two data systems and never holds two copies of the environment's data.
	client          sdks.LDClientContext
	logger          *slog.Logger
	wrapper         *datadestination.DataDestinationWrapper
	identifiers     EnvIdentifiers
	secureMode      bool
	envStreams      *streams.EnvStreams
	streamProviders []streams.StreamProvider
	jsContext       JSClientContext
	evaluator       ldeval.Evaluator
	eventDispatcher *events.EventDispatcher
	bigSegmentSync  bigsegments.BigSegmentSynchronizer
	// makeBigSegmentSync builds a synchronizer for an anchor SDK key. A re-anchor uses it to rebuild
	// the synchronizer, because the synchronizer takes its key at construction and exposes no way to
	// change it. Its HTTP requests already set Authorization per request rather than relying on baked
	// in headers, so re-keying it in place is within reach; what stands in the way is coordinating a
	// backoff reset and a reconnect with the goroutine that owns its retry strategy. Refer to
	// SDK-3200. It is nil when big segments are not configured.
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
	globalLogger              *slog.Logger
	ttl                       time.Duration
	initErr                   error
	creationTime              time.Time
	keyRotator                *credential.Rotator
	stopMonitoringCredentials chan struct{}
	doneMonitoringCredentials chan struct{}
	connectionMapper          ConnectionMapper
	offline                   bool
	closed                    bool

	// reconcileMu serializes reconcileCredentials against itself and against the cleanup ticker. It is
	// held separately from mu so that readers keep running while a reconcile is in progress.
	reconcileMu sync.Mutex

	// pollFlightGroup deduplicates concurrent polling requests for this environment; it is
	// internally synchronized and needs no zero-value setup. It is deliberately not guarded by
	// mu: the field itself is never reassigned.
	pollFlightGroup singleflight.Group
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

	logPrefix := makeLogPrefix(params.LogNameMode, envConfig.SDKKey, envConfig.EnvID)
	envLogger := params.Logger.With("env", logPrefix)

	httpConfig, err := httpconfig.NewHTTPConfig(allConfig.Proxy, allConfig.HTTP, envConfig.SDKKey, params.UserAgent, params.Logger)
	if err != nil {
		return nil, err
	}

	envContext := &envContextImpl{
		identifiers:               params.Identifiers,
		logger:                    envLogger,
		secureMode:                envConfig.SecureMode,
		streamProviders:           params.StreamProviders,
		jsContext:                 params.JSClientContext,
		sdkClientFactory:          params.ClientFactory,
		sdkInitTimeout:            allConfig.Main.InitTimeout.GetOrElse(config.DefaultInitTimeout),
		metricsManager:            params.MetricsManager,
		globalLogger:              params.Logger,
		ttl:                       envConfig.TTL.GetOrElse(0),
		dataStoreInfo:             params.DataStoreInfo,
		creationTime:              time.Now(),
		keyRotator:                credential.NewRotator(params.Logger),
		stopMonitoringCredentials: make(chan struct{}),
		doneMonitoringCredentials: make(chan struct{}),
		connectionMapper:          params.ConnectionMapper,
		offline:                   envConfig.Offline,
	}

	envContext.keyRotator.Initialize(envConfig.SDKKey, envConfig.MobileKey, envConfig.EnvID)

	bigSegmentStoreFactory := params.BigSegmentStoreFactory
	if bigSegmentStoreFactory == nil {
		bigSegmentStoreFactory = bigsegments.DefaultBigSegmentStoreFactory
	}
	bigSegmentStore, err := bigSegmentStoreFactory(envConfig, allConfig, envLogger)
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
		// anchor key. The synchronizer authenticates with the key it is given.
		bigSegmentBaseURI := allConfig.Main.BaseURI.String()
		bigSegmentStreamURI := allConfig.Main.StreamURI.String()
		envContext.makeBigSegmentSync = func(anchor config.SDKKey) bigsegments.BigSegmentSynchronizer {
			return factory(httpConfig, bigSegmentStore, bigSegmentBaseURI, bigSegmentStreamURI,
				envConfig.EnvID, anchor, envLogger, logPrefix)
		}
		envContext.bigSegmentSync = envContext.makeBigSegmentSync(envConfig.SDKKey)
		thingsToCleanUp.AddFunc(envContext.bigSegmentSync.Close)
		envContext.consumeBigSegmentUpdates(envContext.bigSegmentSync)
		// We deliberate do not call bigSegmentSync.Start() here because we don't want the synchronizer to
		// start until we know that at least one big segment exists. That's implemented by the
		// envContextStreamUpdates methods.
	}

	envStreams := streams.NewEnvStreams(
		params.StreamProviders,
		envContextStoreQueries{envContext},
		allConfig.Main.HeartbeatInterval.GetOrElse(config.DefaultHeartbeatInterval),
		envLogger,
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

	wrapper := datadestination.NewDataDesinationWrapper(envStreamUpdates)
	envContext.wrapper = wrapper

	streamURI := allConfig.Main.StreamURI.String() // config.ValidateConfig has ensured that this has a value
	baseURI := allConfig.Main.BaseURI.String()
	eventsURI := allConfig.Events.EventsURI.String() // ditto

	// Unlike our SDKs, the relay proxy does not provide an option to disable
	// diagnostic events. However, we must still honor the offline mode where 0
	// outbound connections will be made.
	enableDiagnostics := !offlineMode
	var em *metrics.EnvironmentManager
	if params.MetricsManager != nil {
		if enableDiagnostics {
			eventsPublisher, err := events.NewHTTPEventPublisher(envConfig.SDKKey, httpConfig, envLogger,
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

	// Create an EventMetrics recorder for the event dispatchers to use when reporting
	// internal metrics like dropped events. This must be done after the EnvironmentManager
	// is created so we have access to the environment-level OTEL attributes.
	var eventMetrics events.EventMetrics
	if em != nil {
		eventMetrics = em.NewEventMetricsRecorder(params.MetricsManager.GetInstruments())
	}

	var eventDispatcher *events.EventDispatcher
	if allConfig.Events.SendEvents {
		if offlineMode {
			envLogger.Info("events will be accepted for this environment, but will be discarded, since offline mode is enabled")
		} else {
			envLogger.Info("proxying events for this environment")
			eventDispatcher = events.NewEventDispatcher(
				envConfig.SDKKey,
				envConfig.MobileKey,
				envConfig.EnvID,
				envLogger,
				allConfig.Events,
				httpConfig,
				wrapper,
				0, // 0 here means "use the default interval for any periodic cleanup task you may need to run"
				eventMetrics,
			)
		}
	}
	envContext.eventDispatcher = eventDispatcher

	disconnectedStatusTime := allConfig.Main.DisconnectedStatusTime.GetOrElse(config.DefaultDisconnectedStatusTime)

	streamingBuilder := ldcomponents.StreamingDataSourceV2().BaseURI(streamURI)
	pollingBuilder := ldcomponents.PollingDataSourceV2().BaseURI(baseURI)
	fallbackBuilder := ldcomponents.FDv1PollingDataSourceV2().BaseURI(baseURI)

	dataSystemBuilder := ldcomponents.DataSystem().
		Custom().
		Initializers(pollingBuilder.AsInitializer()).
		Synchronizers(streamingBuilder, pollingBuilder).
		FDv1CompatibleSynchronizer(fallbackBuilder)

	if params.DataStoreFactory != nil {
		dataSystemBuilder.DataStore(params.DataStoreFactory, subsystems.DataStoreModeReadWrite)
	}

	config := ld.Config{
		DataSystem: dataSystemBuilder,
		LDRelayDataDestination: func(ro subsystems.ReadOnlyDataStore, changeSetUpdates <-chan subsystems.ChangeSet) {
			wrapper.SetDataSystemPieces(ro, changeSetUpdates)
		},
		DiagnosticOptOut: !enableDiagnostics,
		Events:           ldcomponents.SendEvents().EnableGzip(true),
		HTTP:             httpConfig.SDKHTTPConfigFactory,
		Logging: ldcomponents.Logging().
			Loggers(logging.NewLDLogBridge(envLogger)).
			LogDataSourceOutageAsErrorAfter(disconnectedStatusTime),
		ServiceEndpoints: interfaces.ServiceEndpoints{
			Events: eventsURI,
		},
	}

	envContext.sdkConfig = config

	// If appropriate, create the SDK subcomponent that will be used for flag evaluations. We're
	// creating and managing it separately from the full SDK instance that we'll be creating (in
	// startSDKClient) - we use the SDK instance only for talking to LaunchDarkly and populating
	// the data store, not for evaluating flags, because Relay needs to customize the evaluation
	// behavior. The other component we need for evaluations is the Evaluator, but we can't create
	// that one we get to startSDKClient because it has to be hooked up to the SDK's data store.
	if bigSegmentStore != nil {
		configFactory := params.SDKBigSegmentsConfigFactory
		if configFactory == nil {
			configFactory, err = sdks.ConfigureBigSegments(allConfig, envConfig, params.Logger)
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
				logging.NewLDLogBridge(envLogger),
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

// addCredential makes cred authenticate downstream traffic for this environment. It never touches the
// SDK client: the anchor owns the upstream connection, and only reanchor moves it.
func (c *envContextImpl) addCredential(newCredential credential.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registerCredentialMappings(newCredential)

	// Event forwarding collapses to one mobile key, so only the primary repoints the dispatcher. A
	// non-primary accepted mobile key authenticates downstream traffic but forwards nothing.
	if mobileKey, ok := newCredential.(config.MobileKey); ok && mobileKey == c.keyRotator.MobileKey() {
		if c.eventDispatcher != nil {
			c.eventDispatcher.ReplaceCredential(mobileKey)
		}
	}
}

// registerCredentialMappings registers cred with the environment's stream machinery and adds the
// connection-to-environment mapping, so a connection authenticating with cred reaches this
// environment. Stream handlers are built per request, in GetStreamHandlerV1 and GetStreamHandlerV2.
// The caller must hold c.mu.
func (c *envContextImpl) registerCredentialMappings(cred credential.SDKCredential) {
	c.envStreams.AddCredential(cred)
	c.connectionMapper.AddConnectionMapping(cred, c)
}

// removeCredential stops cred from authenticating downstream traffic for this environment.
//
// It deliberately does NOT close the SDK client. The client is no longer tied to the key it was built
// with -- a re-anchor re-keys it in place -- so closing it here would tear down the environment's
// only upstream connection whenever any SDK key was revoked, the environment's original key
// included, once it has been rotated away from.
func (c *envContextImpl) removeCredential(oldCredential credential.SDKCredential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectionMapper.RemoveConnectionMapping(oldCredential)
	c.envStreams.RemoveCredential(oldCredential)
}

func (c *envContextImpl) startSDKClient(sdkKey config.SDKKey, readyCh chan<- EnvContext, suppressErrors bool) {
	client, err := c.sdkClientFactory(sdkKey, c.sdkConfig, c.sdkInitTimeout)
	c.mu.Lock()
	name := c.identifiers.GetDisplayName()
	if client != nil {
		c.client = client
		// A reconcile can move the anchor while this build is in flight, and the build used the key it
		// was started with. Catch the client up so it does not connect on a superseded key.
		if anchor := c.keyRotator.AnchorKey(); !c.offline && anchor.Defined() && anchor != sdkKey {
			if err := client.SetSDKKey(string(anchor)); err != nil {
				c.globalLogger.Error("could not apply the current SDK key to a newly built client",
					"env", name, "error", err)
			}
		}

		// The data store instance is created by the SDK when it creates the client. Now that
		// we have a data store, we can finish setting up the Evaluator that we'll use for this
		// environment.
		store := c.wrapper.GetReadOnlyStore()
		dataProvider := ldstoreimpl.NewDataStoreEvaluatorDataProvider(store, logging.NewLDLogBridge(c.logger))
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
			c.globalLogger.Warn("ignoring error initializing LaunchDarkly client", "env", name, "error", err)
		} else {
			c.globalLogger.Error("error initializing LaunchDarkly client", "env", name, "error", err)
			if readyCh != nil {
				readyCh <- c
			}
			return
		}
	} else {
		c.globalLogger.Info("initialized LaunchDarkly client", "env", name, "sdkKey", sdkKey.Masked())
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

func (c *envContextImpl) ReconcileCredentials(newSet credential.AcceptedSet) {
	c.reconcileCredentials(newSet, time.Now())
}

// reconcileCredentials is the time-injectable implementation of ReconcileCredentials. now is the
// reference time for expiry arithmetic.
//
// reconcileMu serializes it against concurrent reconciles and against the cleanup ticker, so that
// the ticker cannot steal additions a reconcile has just queued.
func (c *envContextImpl) reconcileCredentials(newSet credential.AcceptedSet, now time.Time) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	c.applyCredentialSet(newSet, now)
}

// applyCredentialSet moves the environment to newSet: add, re-anchor, remove. The caller must hold
// reconcileMu.
//
// Adding first registers the incoming keys' mappings. The re-anchor then moves the upstream
// connection while the outgoing anchor still authenticates downstream traffic. Removal comes last,
// so a revoked key keeps working until the connection has moved.
func (c *envContextImpl) applyCredentialSet(newSet credential.AcceptedSet, now time.Time) {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		// The environment is being torn down, so there is nothing left to reconcile against.
		return
	}

	result := c.keyRotator.Reconcile(newSet, now)
	additions, expirations := c.keyRotator.StepTime(now)

	for _, cred := range additions {
		c.addCredential(cred)
	}

	if result.AnchorChange != nil && !c.reanchor(result.AnchorChange) {
		// The re-anchor did not happen. Undo only the anchor change; every other change in this
		// payload stands. A brand-new anchor had its mappings registered by reanchor, so take them
		// back down.
		if !result.AnchorChange.NewAnchorPreviouslyAccepted {
			c.removeCredential(result.AnchorChange.NewAnchor)
		}
		c.keyRotator.RevertAnchorChange(*result.AnchorChange)
		// Keep the outgoing anchor serving by not expiring it here, even if this payload revoked it.
		previousAnchor := result.AnchorChange.PreviousAnchor
		expirations = slices.DeleteFunc(expirations, func(cred credential.SDKCredential) bool {
			return cred == previousAnchor
		})
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

// reanchor moves the environment's upstream connection to change.NewAnchor by re-keying the existing
// SDK client. It reports whether the anchor moved.
//
// There is no client to build and so nothing to roll back on a transient failure: the only way
// SetSDKKey fails is a key that is not valid in an HTTP header, which no retry will fix. That is a
// configuration error, so the environment parks on its current anchor and says so loudly, and the
// next auto-configuration payload supplies a new key.
//
// An offline environment has no upstream connection, so re-anchoring it is only the mapping and
// designation changes.
func (c *envContextImpl) reanchor(change *credential.AnchorChange) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return false
	}

	// Mappings can exist without the anchor owning them, but never the reverse. Reconcile stripped a
	// brand-new anchor from additions, so register it here; an already-accepted key has its mappings.
	if !change.NewAnchorPreviouslyAccepted {
		c.registerCredentialMappings(change.NewAnchor)
	}

	if !c.offline && c.client != nil {
		if err := c.client.SetSDKKey(string(change.NewAnchor)); err != nil {
			c.globalLogger.Error("could not move the environment to its new SDK key; "+
				"keeping the previous key, which LaunchDarkly may already be invalidating",
				"env", c.identifiers.GetDisplayName(),
				"previous", change.PreviousAnchor.Masked(),
				"new", change.NewAnchor.Masked(),
				"error", err)
			return false
		}
	}

	c.keyRotator.CommitAnchor(change.NewAnchor)

	if c.metricsEventPub != nil {
		c.metricsEventPub.ReplaceCredential(change.NewAnchor)
	}
	if c.eventDispatcher != nil {
		c.eventDispatcher.ReplaceCredential(change.NewAnchor)
	}

	// Big-segment requests authenticate with the anchor, so the synchronizer follows it.
	c.reanchorBigSegmentSync(change.NewAnchor)

	c.globalLogger.Info("moved the environment to a new SDK key",
		"env", c.identifiers.GetDisplayName(),
		"previous", change.PreviousAnchor.Masked(),
		"new", change.NewAnchor.Masked())
	return true
}

// triggerCredentialChanges drains the rotator's queue and applies the additions and expirations. It
// runs on the cleanup ticker, so it can fire while a reconcile is in flight; reconcileMu keeps the
// two from interleaving. reconcileCredentials never calls it, so there is no re-entrancy.
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

func (c *envContextImpl) GetAcceptedKeys() credential.AcceptedKeySet {
	return c.keyRotator.AcceptedKeys()
}

func (c *envContextImpl) GetDeprecatedCredentials() []credential.SDKCredential {
	return c.keyRotator.DeprecatedCredentials()
}

func (c *envContextImpl) GetClient() sdks.LDClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *envContextImpl) GetStore() subsystems.ReadOnlyDataStore {
	return c.wrapper.GetReadOnlyStore()
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

func (c *envContextImpl) GetLogger() *slog.Logger {
	return c.logger
}

func (c *envContextImpl) GetStreamHandlerV1(streamProvider streams.StreamProvider, cred credential.SDKCredential) http.Handler {
	if !c.acceptsForStream(cred) {
		return http.HandlerFunc(invalidStreamHandler)
	}
	if h := streamProvider.HandlerV1(cred); h != nil {
		return h
	}
	return http.HandlerFunc(invalidStreamHandler)
}

func (c *envContextImpl) GetStreamHandlerV2(streamProvider streams.StreamProvider, cred credential.SDKCredential) http.Handler {
	if !c.acceptsForStream(cred) {
		return http.HandlerFunc(invalidStreamHandler)
	}
	if h := streamProvider.HandlerV2(cred); h != nil {
		return h
	}
	return http.HandlerFunc(invalidStreamHandler)
}

// acceptsForStream re-checks the accepted set before a stream handler is built.
//
// Handlers used to be built once per credential and cached in a map that add and remove had to keep
// in step. That map was what made revocation racy: a handler stayed in it until the removal was
// processed, so a request that authenticated before a revocation still found a working handler.
// Building per request is what creates a place to ask the question again.
//
// The build is cheap enough to do per connect. Measured: the client-side path costs 13ns with no
// allocations, which is faster than the two-level map lookup it replaced, and the heaviest provider
// -- the server-side V2 handler, which wraps an init deadline and a basis-header closure -- costs
// 105ns and 96 bytes. Both are invisible next to the SSE handshake and payload send that follow.
//
// The middleware authenticates the credential once, at the start of the request, and a credential can
// be revoked while that request is still in flight: on the REPORT stream endpoints the client paces
// the body read that precedes this call, so the window is as long as the client wants. The stream
// providers only type-check the credential, so a revoked one would otherwise be handed a working
// handler rather than a 404.
//
// A revocation can still land between this check and the subscription registering. The eventsource
// handler writes the status line first, so that case still answers 200.
//
// The rotator guards its own accepted set, so this deliberately does not take c.mu: a reconcile holds
// that lock across its whole re-anchor, and stream connects must not queue behind it.
func (c *envContextImpl) acceptsForStream(cred credential.SDKCredential) bool {
	return c.keyRotator.IsAccepted(cred)
}

func (c *envContextImpl) GetPollingFlightGroup() *singleflight.Group {
	return &c.pollFlightGroup
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

func (c *envContextImpl) GetMetricsEnv() *metrics.EnvironmentManager {
	return c.metricsEnv
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
	if c.client != nil {
		_ = c.client.Close()
		c.client = nil
	}
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

// consumeBigSegmentUpdates spawns a goroutine that drains sync's update channel and broadcasts a
// cache clear and a client-side invalidation for each batch. The goroutine ends when sync closes.
//
// The batch's segment keys are not needed: relay pings every connected client-side SDK rather than
// working out which flags to re-evaluate.
func (c *envContextImpl) consumeBigSegmentUpdates(sync bigsegments.BigSegmentSynchronizer) {
	ch := sync.SegmentUpdatesCh()
	if ch == nil {
		return
	}
	go func() {
		for range ch {
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
// bakes in its SDK key and is not restartable, so a re-anchor replaces it. The replacement is started
// only when the outgoing one had been started. The caller must hold c.mu.
func (c *envContextImpl) reanchorBigSegmentSync(newAnchor config.SDKKey) {
	if c.bigSegmentSync == nil {
		return
	}
	wasStarted := c.bigSegmentsExist
	outgoing := c.bigSegmentSync
	c.bigSegmentSync = c.makeBigSegmentSync(newAnchor)
	c.consumeBigSegmentUpdates(c.bigSegmentSync)
	if wasStarted {
		c.bigSegmentSync.Start()
	}
	outgoing.Close()
}

// bigSegmentSyncConfigured reports whether this environment has a big-segment synchronizer. The read
// is synchronized against reanchorBigSegmentSync's reassignment, because the store-update sink runs
// on the SDK's data-source goroutine while a re-anchor runs on the reconcile goroutine.
func (c *envContextImpl) bigSegmentSyncConfigured() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bigSegmentSync != nil
}

func (c *envContextImpl) setBigSegmentsExist() {
	c.mu.Lock()
	firstTime := !c.bigSegmentsExist
	c.bigSegmentsExist = true
	// Start the synchronizer while holding the lock. Starting it afterwards could start one that a
	// concurrent re-anchor has already retired. Start only launches a goroutine, so holding c.mu is
	// safe here.
	started := firstTime && c.bigSegmentSync != nil
	if started {
		c.bigSegmentSync.Start()
	}
	c.mu.Unlock()

	if started {
		c.sdkBigSegments.SetPollingActive(true) // has no effect if already active
	}
}

func (q envContextStoreQueries) IsInitialized() bool {
	if s := q.context.wrapper.GetReadOnlyStore(); s != nil {
		return s.IsInitialized()
	}
	return false
}

func (q envContextStoreQueries) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	if s := q.context.wrapper.GetReadOnlyStore(); s != nil {
		return s.Snapshot()
	}
	return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{}, subsystems.NoSelector(), nil
}

func (q envContextStoreQueries) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	if s := q.context.wrapper.GetReadOnlyStore(); s != nil {
		return s.GetAll(kind)
	}
	return nil, nil
}

func (u *envContextStreamUpdates) handleBigSegments(events []subsystems.Change) {
	if !u.context.bigSegmentSyncConfigured() {
		return
	}

	hasBigSegment := false
	for _, event := range events {
		if event.Kind == subsystems.SegmentKind {
			var segment struct {
				Unbounded bool `json:"unbounded,omitempty"`
			}
			if err := json.Unmarshal(event.Object, &segment); err == nil && segment.Unbounded {
				hasBigSegment = true
				break
			}
		}
	}
	if hasBigSegment {
		u.context.setBigSegmentsExist()
	}
}

func (u *envContextStreamUpdates) Apply(changeSet subsystems.ChangeSet) {
	u.context.envStreams.Apply(changeSet)
	u.handleBigSegments(changeSet.Changes())
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
