// Package ldclient is the main package for the LaunchDarkly SDK.
//
// This package contains the types and methods that most applications will use. The most commonly
// used other packages in go-server-sdk are "ldcomponents" (configuration builders), and database
// integrations such as "ldredis" and "lddynamodb".
//
// Other types that are commonly used with the SDK are in the go-sdk-common repository
// (https://godoc.org/gopkg.in/launchdarkly/go-sdk-common.v2).
package ldclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	ldeval "gopkg.in/launchdarkly/go-server-sdk-evaluation.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// Version is the client version.
const Version = internal.SDKVersion

// LDClient is the LaunchDarkly client. Client instances are thread-safe.
// Applications should instantiate a single instance for the lifetime
// of their application.
type LDClient struct {
	sdkKey                      string
	loggers                     ldlog.Loggers
	eventProcessor              ldevents.EventProcessor
	dataSource                  interfaces.DataSource
	store                       interfaces.DataStore
	evaluator                   ldeval.Evaluator
	dataSourceStatusBroadcaster *internal.DataSourceStatusBroadcaster
	dataSourceStatusProvider    interfaces.DataSourceStatusProvider
	dataStoreStatusBroadcaster  *internal.DataStoreStatusBroadcaster
	dataStoreStatusProvider     interfaces.DataStoreStatusProvider
	flagChangeEventBroadcaster  *internal.FlagChangeEventBroadcaster
	flagTracker                 interfaces.FlagTracker
	eventsDefault               eventsScope
	eventsWithReasons           eventsScope
	logEvaluationErrors         bool
	offline                     bool
}

// Initialization errors
var (
	ErrInitializationTimeout = errors.New("timeout encountered waiting for LaunchDarkly client initialization")
	ErrInitializationFailed  = errors.New("LaunchDarkly client initialization failed")
	ErrClientNotInitialized  = errors.New("feature flag evaluation called before LaunchDarkly client initialization completed") //nolint:lll
)

// MakeClient creates a new client instance that connects to LaunchDarkly with the default configuration.
//
// For advanced configuration options, use MakeCustomClient.
//
// Unless it is configured to be offline with Config.Offline or ldcomponents.ExternalUpdatesOnly(), the client
// will begin attempting to connect to LaunchDarkly as soon as you call this constructor. The constructor will
// return when it successfully connects, or when the timeout set by the waitFor parameter expires, whichever
// comes first. If it has not succeeded in connecting when the timeout elapses, you will receive the client in
// an uninitialized state where feature flags will return  default values; it will still continue trying to
// connect in the background. You can detect whether initialization has succeeded by calling Initialized().
//
// If you prefer to have the constructor return immediately, and then wait for initialization to finish
// at some other point, you can use GetDataSourceStatusProvider() as follows:
//
//     // create the client but do not wait
//     client = ld.MakeClient(sdkKey, 0)
//
//     // later, possibly on another goroutine:
//     inited := client.GetDataSourceStatusProvider().WaitFor(DataSourceStateValid, 10 * time.Second)
//     if !inited {
//         // do whatever is appropriate if initialization has timed out
//     }
func MakeClient(sdkKey string, waitFor time.Duration) (*LDClient, error) {
	// COVERAGE: this constructor cannot be called in unit tests because it uses the default base
	// URI and will attempt to make a live connection to LaunchDarkly.
	return MakeCustomClient(sdkKey, Config{}, waitFor)
}

// MakeCustomClient creates a new client instance that connects to LaunchDarkly with a custom configuration.
//
// The config parameter allows customization of all SDK properties; some of these are represented directly as fields in
// Config, while others are set by builder methods on a more specific configuration object. For instance, to use polling
// mode instead of streaming, configure the polling interval, and use a non-default HTTP timeout for all HTTP requests:
//
//     config := ld.Config{
//         DataSource: ldcomponents.PollingDataSource().PollInterval(45 * time.Minute),
//         Timeout: 4 * time.Second,
//     }
//     client, err := ld.MakeCustomClient(sdkKey, config, 5 * time.Second)
//
// Unless it is configured to be offline with Config.Offline or ldcomponents.ExternalUpdatesOnly(), the client
// will begin attempting to connect to LaunchDarkly as soon as you call this constructor. The constructor will
// return when it successfully connects, or when the timeout set by the waitFor parameter expires, whichever
// comes first. If it has not succeeded in connecting when the timeout elapses, you will receive the client in
// an uninitialized state where feature flags will return  default values; it will still continue trying to
// connect in the background. You can detect whether initialization has succeeded by calling Initialized().
//
// If you prefer to have the constructor return immediately, and then wait for initialization to finish
// at some other point, you can use GetDataSourceStatusProvider() as follows:
//
//     // create the client but do not wait
//     client = ld.MakeCustomClient(sdkKey, config, 0)
//
//     // later, possibly on another goroutine:
//     inited := client.GetDataSourceStatusProvider().WaitFor(DataSourceStateValid, 10 * time.Second)
//     if !inited {
//         // do whatever is appropriate if initialization has timed out
//     }
func MakeCustomClient(sdkKey string, config Config, waitFor time.Duration) (*LDClient, error) {
	closeWhenReady := make(chan struct{})

	eventProcessorFactory := getEventProcessorFactory(config)

	// Do not create a diagnostics manager if diagnostics are disabled, or if we're not using the standard event processor.
	var diagnosticsManager *ldevents.DiagnosticsManager
	if !config.DiagnosticOptOut {
		if reflect.TypeOf(eventProcessorFactory) == reflect.TypeOf(ldcomponents.SendEvents()) {
			diagnosticsManager = createDiagnosticsManager(sdkKey, config, waitFor)
		}
	}

	clientContext, err := newClientContextFromConfig(sdkKey, config, diagnosticsManager)
	if err != nil {
		return nil, err
	}

	loggers := clientContext.GetLogging().GetLoggers()
	loggers.Infof("Starting LaunchDarkly client %s", Version)

	client := LDClient{
		sdkKey:              sdkKey,
		loggers:             loggers,
		logEvaluationErrors: clientContext.GetLogging().IsLogEvaluationErrors(),
		offline:             config.Offline,
	}

	client.dataStoreStatusBroadcaster = internal.NewDataStoreStatusBroadcaster()
	dataStoreUpdates := internal.NewDataStoreUpdatesImpl(client.dataStoreStatusBroadcaster)
	store, err := getDataStoreFactory(config).CreateDataStore(clientContext, dataStoreUpdates)
	if err != nil {
		return nil, err
	}
	client.store = store

	dataProvider := interfaces.NewDataStoreEvaluatorDataProvider(store, loggers)
	client.evaluator = ldeval.NewEvaluator(dataProvider)
	client.dataStoreStatusProvider = internal.NewDataStoreStatusProviderImpl(store, dataStoreUpdates)

	client.dataSourceStatusBroadcaster = internal.NewDataSourceStatusBroadcaster()
	client.flagChangeEventBroadcaster = internal.NewFlagChangeEventBroadcaster()
	dataSourceUpdates := internal.NewDataSourceUpdatesImpl(
		store,
		client.dataStoreStatusProvider,
		client.dataSourceStatusBroadcaster,
		client.flagChangeEventBroadcaster,
		clientContext.GetLogging().GetLogDataSourceOutageAsErrorAfter(),
		loggers,
	)

	client.eventProcessor, err = eventProcessorFactory.CreateEventProcessor(clientContext)
	if err != nil {
		return nil, err
	}
	if isNullEventProcessorFactory(eventProcessorFactory) {
		client.eventsDefault = newDisabledEventsScope()
		client.eventsWithReasons = newDisabledEventsScope()
	} else {
		client.eventsDefault = newEventsScope(&client, false)
		client.eventsWithReasons = newEventsScope(&client, true)
	}

	dataSource, err := createDataSource(config, clientContext, dataSourceUpdates)
	client.dataSource = dataSource
	if err != nil {
		return nil, err
	}
	client.dataSourceStatusProvider = internal.NewDataSourceStatusProviderImpl(
		client.dataSourceStatusBroadcaster,
		dataSourceUpdates,
	)

	client.flagTracker = internal.NewFlagTrackerImpl(
		client.flagChangeEventBroadcaster,
		func(flagKey string, user lduser.User, defaultValue ldvalue.Value) ldvalue.Value {
			value, _ := client.JSONVariation(flagKey, user, defaultValue)
			return value
		},
	)

	client.dataSource.Start(closeWhenReady)
	if waitFor > 0 && client.dataSource != internal.NewNullDataSource() {
		loggers.Infof("Waiting up to %d milliseconds for LaunchDarkly client to start...",
			waitFor/time.Millisecond)
		timeout := time.After(waitFor)
		for {
			select {
			case <-closeWhenReady:
				if !client.dataSource.IsInitialized() {
					loggers.Warn("LaunchDarkly client initialization failed")
					return &client, ErrInitializationFailed
				}

				loggers.Info("Successfully initialized LaunchDarkly client!")
				return &client, nil
			case <-timeout:
				loggers.Warn("Timeout encountered waiting for LaunchDarkly client initialization")
				go func() { <-closeWhenReady }() // Don't block the DataSource when not waiting
				return &client, ErrInitializationTimeout
			}
		}
	}
	go func() { <-closeWhenReady }() // Don't block the DataSource when not waiting
	return &client, nil
}

func getDataStoreFactory(config Config) interfaces.DataStoreFactory {
	if config.DataStore == nil {
		return ldcomponents.InMemoryDataStore()
	}
	return config.DataStore
}

func createDataSource(
	config Config,
	context interfaces.ClientContext,
	dataSourceUpdates interfaces.DataSourceUpdates,
) (interfaces.DataSource, error) {
	if config.Offline {
		context.GetLogging().GetLoggers().Info("Starting LaunchDarkly client in offline mode")
		dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		return internal.NewNullDataSource(), nil
	}
	factory := config.DataSource
	if factory == nil {
		// COVERAGE: can't cause this condition in unit tests because it would try to connect to production LD
		factory = ldcomponents.StreamingDataSource()
	}
	return factory.CreateDataSource(context, dataSourceUpdates)
}

// Identify reports details about a a user.
func (client *LDClient) Identify(user lduser.User) error {
	if client.eventsDefault.disabled {
		return nil
	}
	if user.GetKey() == "" {
		client.loggers.Warn("Identify called with empty user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	evt := client.eventsDefault.factory.NewIdentifyEvent(ldevents.User(user))
	client.eventProcessor.SendEvent(evt)
	return nil
}

// TrackEvent reports that a user has performed an event.
//
// The eventName parameter is defined by the application and will be shown in analytics reports;
// it normally corresponds to the event name of a metric that you have created through the
// LaunchDarkly dashboard. If you want to associate additional data with this event, use TrackData
// or TrackMetric.
func (client *LDClient) TrackEvent(eventName string, user lduser.User) error {
	return client.TrackData(eventName, user, ldvalue.Null())
}

// TrackData reports that a user has performed an event, and associates it with custom data.
//
// The eventName parameter is defined by the application and will be shown in analytics reports;
// it normally corresponds to the event name of a metric that you have created through the
// LaunchDarkly dashboard.
//
// The data parameter is a value of any JSON type, represented with the ldvalue.Value type, that
// will be sent with the event. If no such value is needed, use ldvalue.Null() (or call TrackEvent
// instead). To send a numeric value for experimentation, use TrackMetric.
func (client *LDClient) TrackData(eventName string, user lduser.User, data ldvalue.Value) error {
	if client.eventsDefault.disabled {
		return nil
	}
	if user.GetKey() == "" {
		client.loggers.Warn("Track called with empty/nil user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	client.eventProcessor.SendEvent(
		client.eventsDefault.factory.NewCustomEvent(
			eventName,
			ldevents.User(user),
			data,
			false,
			0,
		))
	return nil
}

// TrackMetric reports that a user has performed an event, and associates it with a numeric value.
// This value is used by the LaunchDarkly experimentation feature in numeric custom metrics, and will also
// be returned as part of the custom event for Data Export.
//
// The eventName parameter is defined by the application and will be shown in analytics reports;
// it normally corresponds to the event name of a metric that you have created through the
// LaunchDarkly dashboard.
//
// The data parameter is a value of any JSON type, represented with the ldvalue.Value type, that
// will be sent with the event. If no such value is needed, use ldvalue.Null().
func (client *LDClient) TrackMetric(eventName string, user lduser.User, metricValue float64, data ldvalue.Value) error {
	if client.eventsDefault.disabled {
		return nil
	}
	if user.GetKey() == "" {
		client.loggers.Warn("Track called with empty/nil user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	client.eventProcessor.SendEvent(
		client.eventsDefault.factory.NewCustomEvent(
			eventName,
			ldevents.User(user),
			data,
			true,
			metricValue,
		))
	return nil
}

// IsOffline returns whether the LaunchDarkly client is in offline mode.
func (client *LDClient) IsOffline() bool {
	return client.offline
}

// SecureModeHash generates the secure mode hash value for a user
// See https://github.com/launchdarkly/js-client#secure-mode
func (client *LDClient) SecureModeHash(user lduser.User) string {
	key := []byte(client.sdkKey)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(user.GetKey()))
	return hex.EncodeToString(h.Sum(nil))
}

// Initialized returns whether the LaunchDarkly client is initialized.
func (client *LDClient) Initialized() bool {
	return client.dataSource.IsInitialized()
}

// Close shuts down the LaunchDarkly client. After calling this, the LaunchDarkly client
// should no longer be used. The method will block until all pending analytics events (if any)
// been sent.
func (client *LDClient) Close() error {
	client.loggers.Info("Closing LaunchDarkly client")
	_ = client.eventProcessor.Close()
	_ = client.dataSource.Close()
	_ = client.store.Close()
	client.dataSourceStatusBroadcaster.Close()
	client.dataStoreStatusBroadcaster.Close()
	client.flagChangeEventBroadcaster.Close()
	return nil
}

// Flush tells the client that all pending analytics events (if any) should be delivered as soon
// as possible. Flushing is asynchronous, so this method will return before it is complete.
// However, if you call Close(), events are guaranteed to be sent before that method returns.
func (client *LDClient) Flush() {
	client.eventProcessor.Flush()
}

// AllFlagsState returns an object that encapsulates the state of all feature flags for a
// given user, including the flag values and also metadata that can be used on the front end.
// You may pass any combination of ClientSideOnly, WithReasons, and DetailsOnlyForTrackedFlags
// as optional parameters to control what data is included.
//
// The most common use case for this method is to bootstrap a set of client-side feature flags
// from a back-end service.
func (client *LDClient) AllFlagsState(user lduser.User, options ...FlagsStateOption) FeatureFlagsState {
	valid := true
	if client.IsOffline() {
		client.loggers.Warn("Called AllFlagsState in offline mode. Returning empty state")
		valid = false
	} else if !client.Initialized() {
		if client.store.IsInitialized() {
			client.loggers.Warn("Called AllFlagsState before client initialization; using last known values from data store")
		} else {
			client.loggers.Warn("Called AllFlagsState before client initialization. Data store not available; returning empty state") //nolint:lll
			valid = false
		}
	}

	if !valid {
		return FeatureFlagsState{valid: false}
	}

	items, err := client.store.GetAll(interfaces.DataKindFeatures())
	if err != nil {
		client.loggers.Warn("Unable to fetch flags from data store. Returning empty state. Error: " + err.Error())
		return FeatureFlagsState{valid: false}
	}

	state := newFeatureFlagsState()
	clientSideOnly := hasFlagsStateOption(options, ClientSideOnly)
	withReasons := hasFlagsStateOption(options, WithReasons)
	detailsOnlyIfTracked := hasFlagsStateOption(options, DetailsOnlyForTrackedFlags)
	for _, item := range items {
		if item.Item.Item != nil {
			if flag, ok := item.Item.Item.(*ldmodel.FeatureFlag); ok {
				if clientSideOnly && !flag.ClientSide {
					continue
				}
				result := client.evaluator.Evaluate(flag, user, nil)
				var reason ldreason.EvaluationReason
				if withReasons {
					reason = result.Reason
				}
				state.addFlag(*flag, result.Value, result.VariationIndex, reason, detailsOnlyIfTracked)
			}
		}
	}

	return state
}

// BoolVariation returns the value of a boolean feature flag for a given user.
//
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and
// has no off variation.
func (client *LDClient) BoolVariation(key string, user lduser.User, defaultVal bool) (bool, error) {
	detail, err := client.variation(key, user, ldvalue.Bool(defaultVal), true, false)
	return detail.Value.BoolValue(), err
}

// BoolVariationDetail is the same as BoolVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) BoolVariationDetail(
	key string,
	user lduser.User,
	defaultVal bool,
) (bool, ldreason.EvaluationDetail, error) {
	detail, err := client.variation(key, user, ldvalue.Bool(defaultVal), true, true)
	return detail.Value.BoolValue(), detail, err
}

// IntVariation returns the value of a feature flag (whose variations are integers) for the given user.
//
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and
// has no off variation.
//
// If the flag variation has a numeric value that is not an integer, it is rounded toward zero (truncated).
func (client *LDClient) IntVariation(key string, user lduser.User, defaultVal int) (int, error) {
	detail, err := client.variation(key, user, ldvalue.Int(defaultVal), true, false)
	return detail.Value.IntValue(), err
}

// IntVariationDetail is the same as IntVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) IntVariationDetail(
	key string,
	user lduser.User,
	defaultVal int,
) (int, ldreason.EvaluationDetail, error) {
	detail, err := client.variation(key, user, ldvalue.Int(defaultVal), true, true)
	return detail.Value.IntValue(), detail, err
}

// Float64Variation returns the value of a feature flag (whose variations are floats) for the given user.
//
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and
// has no off variation.
func (client *LDClient) Float64Variation(key string, user lduser.User, defaultVal float64) (float64, error) {
	detail, err := client.variation(key, user, ldvalue.Float64(defaultVal), true, false)
	return detail.Value.Float64Value(), err
}

// Float64VariationDetail is the same as Float64Variation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) Float64VariationDetail(
	key string,
	user lduser.User,
	defaultVal float64,
) (float64, ldreason.EvaluationDetail, error) {
	detail, err := client.variation(key, user, ldvalue.Float64(defaultVal), true, true)
	return detail.Value.Float64Value(), detail, err
}

// StringVariation returns the value of a feature flag (whose variations are strings) for the given user.
//
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and has
// no off variation.
func (client *LDClient) StringVariation(key string, user lduser.User, defaultVal string) (string, error) {
	detail, err := client.variation(key, user, ldvalue.String(defaultVal), true, false)
	return detail.Value.StringValue(), err
}

// StringVariationDetail is the same as StringVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) StringVariationDetail(
	key string,
	user lduser.User,
	defaultVal string,
) (string, ldreason.EvaluationDetail, error) {
	detail, err := client.variation(key, user, ldvalue.String(defaultVal), true, true)
	return detail.Value.StringValue(), detail, err
}

// JSONVariation returns the value of a feature flag for the given user, allowing the value to be
// of any JSON type.
//
// The value is returned as an ldvalue.Value, which can be inspected or converted to other types using
// Value methods such as GetType() and BoolValue(). The defaultVal parameter also uses this type. For
// instance, if the values for this flag are JSON arrays:
//
//     defaultValAsArray := ldvalue.BuildArray().
//         Add(ldvalue.String("defaultFirstItem")).
//         Add(ldvalue.String("defaultSecondItem")).
//         Build()
//     result, err := client.JSONVariation(flagKey, user, defaultValAsArray)
//     firstItemAsString := result.GetByIndex(0).StringValue() // "defaultFirstItem", etc.
//
// You can also use unparsed json.RawMessage values:
//
//     defaultValAsRawJSON := ldvalue.Raw(json.RawMessage(`{"things":[1,2,3]}`))
//     result, err := client.JSONVariation(flagKey, user, defaultValAsJSON
//     resultAsRawJSON := result.AsRaw()
//
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) JSONVariation(key string, user lduser.User, defaultVal ldvalue.Value) (ldvalue.Value, error) {
	detail, err := client.variation(key, user, defaultVal, false, false)
	return detail.Value, err
}

// JSONVariationDetail is the same as JSONVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) JSONVariationDetail(
	key string,
	user lduser.User,
	defaultVal ldvalue.Value,
) (ldvalue.Value, ldreason.EvaluationDetail, error) {
	detail, err := client.variation(key, user, defaultVal, false, true)
	return detail.Value, detail, err
}

// GetDataSourceStatusProvider returns an interface for tracking the status of the data source.
//
// The data source is the mechanism that the SDK uses to get feature flag configurations, such as a
// streaming connection (the default) or poll requests. The DataSourceStatusProvider has methods
// for checking whether the data source is (as far as the SDK knows) currently operational and tracking
// changes in this status.
func (client *LDClient) GetDataSourceStatusProvider() interfaces.DataSourceStatusProvider {
	return client.dataSourceStatusProvider
}

// GetDataStoreStatusProvider returns an interface for tracking the status of a persistent data store.
//
// The DataStoreStatusProvider has methods for checking whether the data store is (as far as the SDK
// SDK knows) currently operational, tracking changes in this status, and getting cache statistics. These
// are only relevant for a persistent data store; if you are using an in-memory data store, then this
// method will always report that the store is operational.
func (client *LDClient) GetDataStoreStatusProvider() interfaces.DataStoreStatusProvider {
	return client.dataStoreStatusProvider
}

// GetFlagTracker returns an interface for tracking changes in feature flag configurations.
func (client *LDClient) GetFlagTracker() interfaces.FlagTracker {
	return client.flagTracker
}

// Generic method for evaluating a feature flag for a given user.
func (client *LDClient) variation(
	key string,
	user lduser.User,
	defaultVal ldvalue.Value,
	checkType bool,
	sendReasonsInEvents bool,
) (ldreason.EvaluationDetail, error) {
	if client.IsOffline() {
		return newEvaluationError(defaultVal, ldreason.EvalErrorClientNotReady), nil
	}
	eventsScope := client.eventsDefault
	if sendReasonsInEvents {
		eventsScope = client.eventsWithReasons
	}
	result, flag, err := client.evaluateInternal(key, user, defaultVal, eventsScope)
	if err != nil {
		result.Value = defaultVal
		result.VariationIndex = -1
	} else if checkType && defaultVal.Type() != ldvalue.NullType && result.Value.Type() != defaultVal.Type() {
		result = newEvaluationError(defaultVal, ldreason.EvalErrorWrongType)
	}

	if !eventsScope.disabled {
		var evt ldevents.FeatureRequestEvent
		if flag == nil {
			evt = eventsScope.factory.NewUnknownFlagEvent(key, ldevents.User(user), defaultVal, result.Reason)
		} else {
			evt = eventsScope.factory.NewSuccessfulEvalEvent(
				flag,
				ldevents.User(user),
				result.VariationIndex,
				result.Value,
				defaultVal,
				result.Reason,
				"",
			)
		}
		client.eventProcessor.SendEvent(evt)
	}

	return result, err
}

// Performs all the steps of evaluation except for sending the feature request event (the main one;
// events for prerequisites will be sent).
func (client *LDClient) evaluateInternal(
	key string,
	user lduser.User,
	defaultVal ldvalue.Value,
	eventsScope eventsScope,
) (ldreason.EvaluationDetail, *ldmodel.FeatureFlag, error) {
	// THIS IS A HIGH-TRAFFIC CODE PATH so performance tuning is important. Please see CONTRIBUTING.md for guidelines
	// to keep in mind during any changes to the evaluation logic.

	if user.GetKey() == "" {
		client.loggers.Warnf("User key is blank when evaluating flag: %s. Flag evaluation will proceed, but the user will not be stored in LaunchDarkly.", key) //nolint:lll
	}

	var feature *ldmodel.FeatureFlag
	var storeErr error
	var ok bool

	evalErrorResult := func(
		errKind ldreason.EvalErrorKind,
		flag *ldmodel.FeatureFlag,
		err error,
	) (ldreason.EvaluationDetail, *ldmodel.FeatureFlag, error) {
		detail := newEvaluationError(defaultVal, errKind)
		if client.logEvaluationErrors {
			client.loggers.Warn(err)
		}
		return detail, flag, err
	}

	if !client.Initialized() {
		if client.store.IsInitialized() {
			client.loggers.Warn("Feature flag evaluation called before LaunchDarkly client initialization completed; using last known values from data store") //nolint:lll
		} else {
			return evalErrorResult(ldreason.EvalErrorClientNotReady, nil, ErrClientNotInitialized)
		}
	}

	itemDesc, storeErr := client.store.Get(interfaces.DataKindFeatures(), key)

	if storeErr != nil {
		client.loggers.Errorf("Encountered error fetching feature from store: %+v", storeErr)
		detail := newEvaluationError(defaultVal, ldreason.EvalErrorException)
		return detail, nil, storeErr
	}

	if itemDesc.Item != nil {
		feature, ok = itemDesc.Item.(*ldmodel.FeatureFlag)
		if !ok {
			return evalErrorResult(ldreason.EvalErrorException, nil,
				fmt.Errorf(
					"unexpected data type (%T) found in store for feature key: %s. Returning default value",
					itemDesc.Item,
					key,
				))
		}
	} else {
		return evalErrorResult(ldreason.EvalErrorFlagNotFound, nil,
			fmt.Errorf("unknown feature key: %s. Verify that this feature key exists. Returning default value", key))
	}

	detail := client.evaluator.Evaluate(feature, user, eventsScope.prerequisiteEventRecorder)
	if detail.Reason.GetKind() == ldreason.EvalReasonError && client.logEvaluationErrors {
		client.loggers.Warnf("flag evaluation for %s failed with error %s, default value was returned",
			key, detail.Reason.GetErrorKind())
	}
	if detail.IsDefaultValue() {
		detail.Value = defaultVal
		detail.VariationIndex = -1
	}
	return detail, feature, nil
}

func newEvaluationError(jsonValue ldvalue.Value, errorKind ldreason.EvalErrorKind) ldreason.EvaluationDetail {
	return ldreason.EvaluationDetail{
		Value:          jsonValue,
		VariationIndex: -1,
		Reason:         ldreason.NewEvalReasonError(errorKind),
	}
}
