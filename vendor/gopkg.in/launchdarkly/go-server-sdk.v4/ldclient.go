package ldclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// Version is the client version.
const Version = "4.12.0"

// LDClient is the LaunchDarkly client. Client instances are thread-safe.
// Applications should instantiate a single instance for the lifetime
// of their application.
type LDClient struct {
	sdkKey          string
	config          Config
	eventProcessor  EventProcessor
	updateProcessor UpdateProcessor
	store           FeatureStore
}

// Logger is a generic logger interface.
type Logger interface {
	Println(...interface{})
	Printf(string, ...interface{})
}

// UpdateProcessor describes the interface for an object that receives feature flag data.
type UpdateProcessor interface {
	Initialized() bool
	Close() error
	Start(closeWhenReady chan<- struct{})
}

type nullUpdateProcessor struct{}

func (n nullUpdateProcessor) Initialized() bool {
	return true
}

func (n nullUpdateProcessor) Close() error {
	return nil
}

func (n nullUpdateProcessor) Start(closeWhenReady chan<- struct{}) {
	close(closeWhenReady)
}

// Initialization errors
var (
	ErrInitializationTimeout = errors.New("timeout encountered waiting for LaunchDarkly client initialization")
	ErrInitializationFailed  = errors.New("LaunchDarkly client initialization failed")
	ErrClientNotInitialized  = errors.New("feature flag evaluation called before LaunchDarkly client initialization completed")
)

// MakeClient creates a new client instance that connects to LaunchDarkly with the default configuration. In most
// cases, you should use this method to instantiate your client. The optional duration parameter allows callers to
// block until the client has connected to LaunchDarkly and is properly initialized.
func MakeClient(sdkKey string, waitFor time.Duration) (*LDClient, error) {
	return MakeCustomClient(sdkKey, DefaultConfig, waitFor)
}

// MakeCustomClient creates a new client instance that connects to LaunchDarkly with a custom configuration. The optional duration parameter allows callers to
// block until the client has connected to LaunchDarkly and is properly initialized.
func MakeCustomClient(sdkKey string, config Config, waitFor time.Duration) (*LDClient, error) {
	closeWhenReady := make(chan struct{})

	config.BaseUri = strings.TrimRight(config.BaseUri, "/")
	config.EventsUri = strings.TrimRight(config.EventsUri, "/")
	if config.PollInterval < MinimumPollInterval {
		config.PollInterval = MinimumPollInterval
	}
	config.UserAgent = strings.TrimSpace("GoClient/" + Version + " " + config.UserAgent)

	// Our logger configuration logic is a little funny for backward compatibility reasons. We had
	// to continue providing a non-nil logger in DefaultConfig.Logger, but we still want ldlog to
	// use its own default behavior if the app did not specifically override the logger. So if we
	// see that same exact logger instance, we'll ignore it.
	if config.Logger != nil && config.Logger != defaultLogger {
		config.Loggers.SetBaseLogger(config.Logger)
	}
	if config.Logger == nil {
		config.Logger = DefaultConfig.Logger // always set this, in case someone accidentally uses it instead of Loggers
	}
	config.Loggers.Infof("Starting LaunchDarkly client %s", Version)

	if config.FeatureStore == nil {
		factory := config.FeatureStoreFactory
		if factory == nil {
			factory = NewInMemoryFeatureStoreFactory()
		}
		store, err := factory(config)
		if err != nil {
			return nil, err
		}
		config.FeatureStore = store
	}

	defaultHTTPClient := config.newHTTPClient()

	client := LDClient{
		sdkKey: sdkKey,
		config: config,
		store:  config.FeatureStore,
	}

	if config.EventProcessor != nil {
		client.eventProcessor = config.EventProcessor
	} else if config.SendEvents && !config.Offline {
		client.eventProcessor = NewDefaultEventProcessor(sdkKey, config, defaultHTTPClient)
	} else {
		client.eventProcessor = newNullEventProcessor()
	}

	if config.UpdateProcessor != nil {
		client.updateProcessor = config.UpdateProcessor
	} else {
		factory := config.UpdateProcessorFactory
		if factory == nil {
			factory = createDefaultUpdateProcessor(defaultHTTPClient)
		}
		var err error
		client.updateProcessor, err = factory(sdkKey, config)
		if err != nil {
			return nil, err
		}
	}
	client.updateProcessor.Start(closeWhenReady)
	if waitFor > 0 && !config.Offline && !config.UseLdd {
		config.Loggers.Infof("Waiting up to %d milliseconds for LaunchDarkly client to start...",
			waitFor/time.Millisecond)
	}
	timeout := time.After(waitFor)
	for {
		select {
		case <-closeWhenReady:
			if !client.updateProcessor.Initialized() {
				config.Loggers.Warn("LaunchDarkly client initialization failed")
				return &client, ErrInitializationFailed
			}

			config.Loggers.Info("Successfully initialized LaunchDarkly client!")
			return &client, nil
		case <-timeout:
			if waitFor > 0 {
				config.Loggers.Warn("Timeout encountered waiting for LaunchDarkly client initialization")
				return &client, ErrInitializationTimeout
			}

			go func() { <-closeWhenReady }() // Don't block the UpdateProcessor when not waiting
			return &client, nil
		}
	}
}

func createDefaultUpdateProcessor(httpClient *http.Client) func(string, Config) (UpdateProcessor, error) {
	return func(sdkKey string, config Config) (UpdateProcessor, error) {
		if config.Offline {
			config.Loggers.Info("Started LaunchDarkly client in offline mode")
			return nullUpdateProcessor{}, nil
		}
		if config.UseLdd {
			config.Loggers.Info("Started LaunchDarkly client in LDD mode")
			return nullUpdateProcessor{}, nil
		}
		requestor := newRequestor(sdkKey, config, httpClient)
		if config.Stream {
			return newStreamProcessor(sdkKey, config, requestor), nil
		}
		config.Loggers.Warn("You should only disable the streaming API if instructed to do so by LaunchDarkly support")
		return newPollingProcessor(config, requestor), nil
	}
}

// Identify reports details about a a user.
func (client *LDClient) Identify(user User) error {
	if user.Key == nil || *user.Key == "" {
		client.config.Loggers.Warn("Identify called with empty/nil user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	evt := NewIdentifyEvent(user)
	client.eventProcessor.SendEvent(evt)
	return nil
}

// Track reports that a user has performed an event. Custom data can be attached to the
// event, and is serialized to JSON using the encoding/json package (http://golang.org/pkg/encoding/json/).
func (client *LDClient) Track(key string, user User, data interface{}) error {
	if user.Key == nil || *user.Key == "" {
		client.config.Loggers.Warn("Track called with empty/nil user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	evt := NewCustomEvent(key, user, data)
	client.eventProcessor.SendEvent(evt)
	return nil
}

// TrackWithMetric reports that a user has performed an event, and associates it with a numeric value.
// This value is used by the LaunchDarkly experimentation feature in numeric custom metrics, and will also
// be returned as part of the custom event for Data Export.
//
// As of this version’s release date, the LaunchDarkly service does not support the metricValue attribute.
// As a result, calling TrackWithMetric will not yet produce any different behavior than Track. Refer to
// the SDK reference guide for the latest status: https://docs.launchdarkly.com/docs/go-sdk-reference#section-track
//
// Custom data can also be attached to the event, and is serialized to JSON using the encoding/json package (http://golang.org/pkg/encoding/json/).
func (client *LDClient) TrackWithMetric(key string, user User, data interface{}, metricValue float64) error {
	if user.Key == nil || *user.Key == "" {
		client.config.Loggers.Warnf("TrackWithMetric called with empty/nil user key!")
		return nil // Don't return an error value because we didn't in the past and it might confuse users
	}
	client.eventProcessor.SendEvent(newCustomEvent(key, user, data, &metricValue))
	return nil
}

// IsOffline returns whether the LaunchDarkly client is in offline mode.
func (client *LDClient) IsOffline() bool {
	return client.config.Offline
}

// SecureModeHash generates the secure mode hash value for a user
// See https://github.com/launchdarkly/js-client#secure-mode
func (client *LDClient) SecureModeHash(user User) string {
	if user.Key == nil {
		return ""
	}
	key := []byte(client.sdkKey)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(*user.Key))
	return hex.EncodeToString(h.Sum(nil))
}

// Initialized returns whether the LaunchDarkly client is initialized.
func (client *LDClient) Initialized() bool {
	return client.IsOffline() || client.config.UseLdd || client.updateProcessor.Initialized()
}

// Close shuts down the LaunchDarkly client. After calling this, the LaunchDarkly client
// should no longer be used. The method will block until all pending analytics events (if any)
// been sent.
func (client *LDClient) Close() error {
	client.config.Loggers.Info("Closing LaunchDarkly client")
	if client.IsOffline() {
		return nil
	}
	_ = client.eventProcessor.Close()
	if !client.config.UseLdd {
		_ = client.updateProcessor.Close()
	}
	return nil
}

// Flush tells the client that all pending analytics events (if any) should be delivered as soon
// as possible. Flushing is asynchronous, so this method will return before it is complete.
// However, if you call Close(), events are guaranteed to be sent before that method returns.
func (client *LDClient) Flush() {
	client.eventProcessor.Flush()
}

// AllFlags returns a map from feature flag keys to values for
// a given user. If the result of the flag's evaluation would
// result in the default value, `nil` will be returned. This method
// does not send analytics events back to LaunchDarkly
//
// Deprecated: Use AllFlagsState instead. Current versions of the client-side SDK
// will not generate analytics events correctly if you pass the result of AllFlags.
func (client *LDClient) AllFlags(user User) map[string]interface{} {
	state := client.AllFlagsState(user)
	return state.ToValuesMap()
}

// AllFlagsState returns an object that encapsulates the state of all feature flags for a
// given user, including the flag values and also metadata that can be used on the front end.
// You may pass any combination of ClientSideOnly, WithReasons, and DetailsOnlyForTrackedFlags
// as optional parameters to control what data is included.
//
// The most common use case for this method is to bootstrap a set of client-side feature flags
// from a back-end service.
func (client *LDClient) AllFlagsState(user User, options ...FlagsStateOption) FeatureFlagsState {
	valid := true
	if client.IsOffline() {
		client.config.Loggers.Warn("Called AllFlagsState in offline mode. Returning empty state")
		valid = false
	} else if user.Key == nil {
		client.config.Loggers.Warn("Called AllFlagsState with nil user key. Returning empty state")
		valid = false
	} else if !client.Initialized() {
		if client.store.Initialized() {
			client.config.Loggers.Warn("Called AllFlagsState before client initialization; using last known values from feature store")
		} else {
			client.config.Loggers.Warn("Called AllFlagsState before client initialization. Feature store not available; returning empty state")
			valid = false
		}
	}

	if !valid {
		return FeatureFlagsState{valid: false}
	}

	items, err := client.store.All(Features)
	if err != nil {
		client.config.Loggers.Warn("Unable to fetch flags from feature store. Returning empty state. Error: " + err.Error())
		return FeatureFlagsState{valid: false}
	}

	state := newFeatureFlagsState()
	clientSideOnly := hasFlagsStateOption(options, ClientSideOnly)
	withReasons := hasFlagsStateOption(options, WithReasons)
	detailsOnlyIfTracked := hasFlagsStateOption(options, DetailsOnlyForTrackedFlags)
	for _, item := range items {
		if flag, ok := item.(*FeatureFlag); ok {
			if clientSideOnly && !flag.ClientSide {
				continue
			}
			result, _ := flag.EvaluateDetail(user, client.store, false)
			var reason EvaluationReason
			if withReasons {
				reason = result.Reason
			}
			state.addFlag(flag, result.Value, result.VariationIndex, reason, detailsOnlyIfTracked)
		}
	}

	return state
}

// BoolVariation returns the value of a boolean feature flag for a given user. Returns defaultVal if
// there is an error, if the flag doesn't exist, the client hasn't completed initialization,
// or the feature is turned off and has no off variation.
func (client *LDClient) BoolVariation(key string, user User, defaultVal bool) (bool, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(true), false)
	result, _ := detail.Value.(bool)
	return result, err
}

// BoolVariationDetail is the same as BoolVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) BoolVariationDetail(key string, user User, defaultVal bool) (bool, EvaluationDetail, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(true), true)
	result, _ := detail.Value.(bool)
	return result, detail, err
}

// IntVariation returns the value of a feature flag (whose variations are integers) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and
// has no off variation.
//
// If the flag variation has a numeric value that is not an integer, it is rounded toward zero (truncated).
func (client *LDClient) IntVariation(key string, user User, defaultVal int) (int, error) {
	detail, err := client.variationWithType(key, user, float64(defaultVal), reflect.TypeOf(float64(0)), false)
	result, _ := detail.Value.(float64)
	return int(result), err
}

// IntVariationDetail is the same as IntVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
//
// If the flag variation has a numeric value that is not an integer, it is rounded toward zero (truncated).
func (client *LDClient) IntVariationDetail(key string, user User, defaultVal int) (int, EvaluationDetail, error) {
	detail, err := client.variationWithType(key, user, float64(defaultVal), reflect.TypeOf(float64(0)), true)
	result, _ := detail.Value.(float64)
	return int(result), detail, err
}

// Float64Variation returns the value of a feature flag (whose variations are floats) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and
// has no off variation.
func (client *LDClient) Float64Variation(key string, user User, defaultVal float64) (float64, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(float64(0)), false)
	result, _ := detail.Value.(float64)
	return result, err
}

// Float64VariationDetail is the same as Float64Variation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) Float64VariationDetail(key string, user User, defaultVal float64) (float64, EvaluationDetail, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(float64(0)), true)
	result, _ := detail.Value.(float64)
	return result, detail, err
}

// StringVariation returns the value of a feature flag (whose variations are strings) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off and has
// no off variation.
func (client *LDClient) StringVariation(key string, user User, defaultVal string) (string, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(string("string")), false)
	result, _ := detail.Value.(string)
	return result, err
}

// StringVariationDetail is the same as StringVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) StringVariationDetail(key string, user User, defaultVal string) (string, EvaluationDetail, error) {
	detail, err := client.variationWithType(key, user, defaultVal, reflect.TypeOf(string("string")), true)
	result, _ := detail.Value.(string)
	return result, detail, err
}

// JsonVariation returns the value of a feature flag (whose variations are JSON) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) JsonVariation(key string, user User, defaultVal json.RawMessage) (json.RawMessage, error) {
	detail, err := client.variation(key, user, defaultVal, false)
	if err != nil {
		return defaultVal, err
	}
	valueJSONRawMessage, err := ToJsonRawMessage(detail.Value)
	if err != nil {
		return defaultVal, err
	}
	return valueJSONRawMessage, nil
}

// JsonVariationDetail is the same as JsonVariation, but also returns further information about how
// the value was calculated. The "reason" data will also be included in analytics events.
func (client *LDClient) JsonVariationDetail(key string, user User, defaultVal json.RawMessage) (json.RawMessage, EvaluationDetail, error) {
	detail, err := client.variation(key, user, defaultVal, true)
	if err != nil {
		return defaultVal, detail, err
	}
	valueJSONRawMessage, err := ToJsonRawMessage(detail.Value)
	if err != nil {
		detail.Value = defaultVal
		return defaultVal, detail, err
	}
	return valueJSONRawMessage, detail, nil
}

// Generic method for evaluating a feature flag for a given user. The type of the returned interface{}
// will always be expectedType or the actual defaultValue will be returned.
func (client *LDClient) variationWithType(key string, user User, defaultVal interface{}, expectedType reflect.Type, sendReasonsInEvents bool) (EvaluationDetail, error) {
	result, err := client.variation(key, user, defaultVal, sendReasonsInEvents)
	if err != nil && result.Value != nil {
		valueType := reflect.TypeOf(result.Value)
		if expectedType != valueType {
			result.Value = defaultVal
			result.VariationIndex = nil
			result.Reason = newEvalReasonError(EvalErrorWrongType)
		}
	}
	return result, err
}

// Generic method for evaluating a feature flag for a given user.
func (client *LDClient) variation(key string, user User, defaultVal interface{}, sendReasonsInEvents bool) (EvaluationDetail, error) {
	if client.IsOffline() {
		return EvaluationDetail{Value: defaultVal, Reason: newEvalReasonError(EvalErrorClientNotReady)}, nil
	}
	result, flag, err := client.evaluateInternal(key, user, defaultVal, sendReasonsInEvents)
	if err != nil {
		result.Value = defaultVal
		result.VariationIndex = nil
	}

	var evt FeatureRequestEvent
	if flag == nil {
		evt = newUnknownFlagEvent(key, user, defaultVal, result.Reason, sendReasonsInEvents)
	} else {
		evt = newSuccessfulEvalEvent(flag, user, result.VariationIndex, result.Value, defaultVal,
			result.Reason, sendReasonsInEvents, nil)
	}
	client.eventProcessor.SendEvent(evt)

	return result, err
}

// Evaluate returns the value of a feature for a specified user
func (client *LDClient) Evaluate(key string, user User, defaultVal interface{}) (interface{}, *int, error) {
	result, _, err := client.evaluateInternal(key, user, defaultVal, false)
	return result.Value, result.VariationIndex, err
}

// Performs all the steps of evaluation except for sending the feature request event (the main one;
// events for prerequisites will be sent).
func (client *LDClient) evaluateInternal(key string, user User, defaultVal interface{}, sendReasonsInEvents bool) (EvaluationDetail, *FeatureFlag, error) {
	if user.Key != nil && *user.Key == "" {
		client.config.Loggers.Warnf("User.Key is blank when evaluating flag: %s. Flag evaluation will proceed, but the user will not be stored in LaunchDarkly.", key)
	}

	var feature *FeatureFlag
	var storeErr error
	var ok bool

	evalErrorResult := func(errKind EvalErrorKind, flag *FeatureFlag, err error) (EvaluationDetail, *FeatureFlag, error) {
		detail := EvaluationDetail{Value: defaultVal, Reason: newEvalReasonError(errKind)}
		if client.config.LogEvaluationErrors {
			client.config.Loggers.Warn(err)
		}
		return detail, flag, err
	}

	if !client.Initialized() {
		if client.store.Initialized() {
			client.config.Loggers.Warn("Feature flag evaluation called before LaunchDarkly client initialization completed; using last known values from feature store")
		} else {
			return evalErrorResult(EvalErrorClientNotReady, nil, ErrClientNotInitialized)
		}
	}

	data, storeErr := client.store.Get(Features, key)

	if storeErr != nil {
		client.config.Loggers.Errorf("Encountered error fetching feature from store: %+v", storeErr)
		detail := EvaluationDetail{Value: defaultVal, Reason: newEvalReasonError(EvalErrorException)}
		return detail, nil, storeErr
	}

	if data != nil {
		feature, ok = data.(*FeatureFlag)
		if !ok {
			return evalErrorResult(EvalErrorException, nil,
				fmt.Errorf("unexpected data type (%T) found in store for feature key: %s. Returning default value", data, key))
		}
	} else {
		return evalErrorResult(EvalErrorFlagNotFound, nil,
			fmt.Errorf("unknown feature key: %s. Verify that this feature key exists. Returning default value", key))
	}

	if user.Key == nil {
		return evalErrorResult(EvalErrorUserNotSpecified, feature,
			fmt.Errorf("user.Key cannot be nil when evaluating flag: %s. Returning default value", key))
	}

	detail, prereqEvents := feature.EvaluateDetail(user, client.store, sendReasonsInEvents)
	if detail.Reason != nil && detail.Reason.GetKind() == EvalReasonError && client.config.LogEvaluationErrors {
		if re, ok := detail.Reason.(EvaluationReasonError); ok {
			client.config.Loggers.Warnf("flag evaluation for %s failed with error %s, default value was returned",
				key, re.ErrorKind)
		}
	}
	if detail.IsDefaultValue() {
		detail.Value = defaultVal
	}
	for _, event := range prereqEvents {
		client.eventProcessor.SendEvent(event)
	}
	return detail, feature, nil
}
