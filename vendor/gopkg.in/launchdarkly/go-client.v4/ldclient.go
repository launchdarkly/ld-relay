package ldclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"time"
)

// Version is the client version
const Version = "4.2.2"

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

// Logger is a generic logger interface
type Logger interface {
	Println(...interface{})
	Printf(string, ...interface{})
}

// Config exposes advanced configuration options for the LaunchDarkly client.
type Config struct {
	BaseUri              string
	StreamUri            string
	EventsUri            string
	Capacity             int
	FlushInterval        time.Duration
	SamplingInterval     int32
	PollInterval         time.Duration
	Logger               Logger
	Timeout              time.Duration
	FeatureStore         FeatureStore
	Stream               bool
	UseLdd               bool
	SendEvents           bool
	Offline              bool
	AllAttributesPrivate bool
	// Set to true if you need to see the full user details in every analytics event.
	InlineUsersInEvents   bool
	PrivateAttributeNames []string
	// An object that is responsible for receiving feature flag updates from LaunchDarkly.
	// If nil, a default implementation will be used depending on the rest of the configuration
	// (streaming, polling, etc.); a custom implementation can be substituted for testing.
	UpdateProcessor UpdateProcessor
	// An object that is responsible for recording or sending analytics events. If nil, a
	// default implementation will be used; a custom implementation can be substituted for testing.
	EventProcessor EventProcessor
	// The number of user keys that the event processor can remember at any one time, so that
	// duplicate user details will not be sent in analytics events.
	UserKeysCapacity int
	// The interval at which the event processor will reset its set of known user keys.
	UserKeysFlushInterval time.Duration
	UserAgent             string
}

// MinimumPollInterval describes the minimum value for Config.PollInterval. If you specify a smaller interval,
// the minimum will be used instead.
const MinimumPollInterval = 30 * time.Second

// UpdateProcessor describes the interface for an update processor
type UpdateProcessor interface {
	Initialized() bool
	Close() error
	Start(closeWhenReady chan<- struct{})
}

// DefaultConfig provides the default configuration options for the LaunchDarkly client.
// The easiest way to create a custom configuration is to start with the
// default config, and set the custom options from there. For example:
//   var config = DefaultConfig
//   config.Capacity = 2000
var DefaultConfig = Config{
	BaseUri:               "https://app.launchdarkly.com",
	StreamUri:             "https://stream.launchdarkly.com",
	EventsUri:             "https://events.launchdarkly.com",
	Capacity:              1000,
	FlushInterval:         5 * time.Second,
	PollInterval:          MinimumPollInterval,
	Logger:                log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags),
	Timeout:               3000 * time.Millisecond,
	Stream:                true,
	FeatureStore:          nil,
	UseLdd:                false,
	SendEvents:            true,
	Offline:               false,
	UserKeysCapacity:      1000,
	UserKeysFlushInterval: 5 * time.Minute,
	UserAgent:             "",
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

	if config.FeatureStore == nil {
		config.FeatureStore = NewInMemoryFeatureStore(config.Logger)
	}

	client := LDClient{
		sdkKey: sdkKey,
		config: config,
		store:  config.FeatureStore,
	}

	if config.Offline {
		config.Logger.Println("Started LaunchDarkly in offline mode")
		client.eventProcessor = newNullEventProcessor()
		return &client, nil
	}

	if config.EventProcessor != nil {
		client.eventProcessor = config.EventProcessor
	} else if config.SendEvents {
		client.eventProcessor = NewDefaultEventProcessor(sdkKey, config, nil)
	} else {
		client.eventProcessor = newNullEventProcessor()
	}

	if config.UseLdd {
		config.Logger.Println("Started LaunchDarkly in LDD mode")
		return &client, nil
	}

	requestor := newRequestor(sdkKey, config)

	if config.UpdateProcessor != nil {
		client.updateProcessor = config.UpdateProcessor
	} else if config.Stream {
		client.updateProcessor = newStreamProcessor(sdkKey, config, requestor)
	} else {
		config.Logger.Println("You should only disable the streaming API if instructed to do so by LaunchDarkly support")
		client.updateProcessor = newPollingProcessor(config, requestor)
	}
	client.updateProcessor.Start(closeWhenReady)
	timeout := time.After(waitFor)
	for {
		select {
		case <-closeWhenReady:
			if !client.updateProcessor.Initialized() {
				return &client, ErrInitializationFailed
			}

			config.Logger.Println("Successfully initialized LaunchDarkly client!")
			return &client, nil
		case <-timeout:
			if waitFor > 0 {
				config.Logger.Println("Timeout exceeded when initializing LaunchDarkly client.")
				return &client, ErrInitializationTimeout
			}

			go func() { <-closeWhenReady }() // Don't block the UpdateProcessor when not waiting
			return &client, nil
		}
	}
}

// Identify reports details about a a user.
func (client *LDClient) Identify(user User) error {
	if client.IsOffline() {
		return nil
	}
	if user.Key == nil || *user.Key == "" {
		client.config.Logger.Printf("WARN: Identify called with empty/nil user key!")
	}
	evt := NewIdentifyEvent(user)
	client.eventProcessor.SendEvent(evt)
	return nil
}

// Track reports that a user has performed an event. Custom data can be attached to the
// event, and is serialized to JSON using the encoding/json package (http://golang.org/pkg/encoding/json/).
func (client *LDClient) Track(key string, user User, data interface{}) error {
	if client.IsOffline() {
		return nil
	}
	if user.Key == nil || *user.Key == "" {
		client.config.Logger.Printf("WARN: Track called with empty/nil user key!")
	}
	evt := NewCustomEvent(key, user, data)
	client.eventProcessor.SendEvent(evt)
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
// should no longer be used.
func (client *LDClient) Close() error {
	client.config.Logger.Println("Closing LaunchDarkly Client")
	if client.IsOffline() {
		return nil
	}
	_ = client.eventProcessor.Close()
	if !client.config.UseLdd {
		_ = client.updateProcessor.Close()
	}
	return nil
}

// Flush immediately flushes queued events.
func (client *LDClient) Flush() {
	client.eventProcessor.Flush()
}

// AllFlags returns a map from feature flag keys to values for
// a given user. If the result of the flag's evaluation would
// result in the default value, `nil` will be returned. This method
// does not send analytics events back to LaunchDarkly
func (client *LDClient) AllFlags(user User) map[string]interface{} {
	if client.IsOffline() {
		client.config.Logger.Println("WARN: Called AllFlags in offline mode. Returning nil map")
		return nil
	}

	if !client.Initialized() {
		if client.store.Initialized() {
			client.config.Logger.Println("WARN: Called AllFlags before client initialization; using last known values from feature store")
		} else {
			client.config.Logger.Println("WARN: Called AllFlags before client initialization. Feature store not available; returning nil map")
			return nil
		}
	}

	if user.Key == nil {
		client.config.Logger.Println("WARN: Called AllFlags with nil user key. Returning nil map")
		return nil
	}

	results := make(map[string]interface{})

	items, err := client.store.All(Features)

	if err != nil {
		client.config.Logger.Println("WARN: Unable to fetch flags from feature store. Returning nil map. Error: " + err.Error())
		return nil
	}
	for _, item := range items {
		if flag, ok := item.(*FeatureFlag); ok {
			result, _, _ := client.evalFlag(*flag, user)
			results[flag.Key] = result
		}
	}

	return results
}

func (client *LDClient) evalFlag(flag FeatureFlag, user User) (interface{}, *int, []FeatureRequestEvent) {
	return flag.Evaluate(user, client.store)
}

// BoolVariation returns the value of a boolean feature flag for a given user. Returns defaultVal if
// there is an error, if the flag doesn't exist, the client hasn't completed initialization,
// or the feature is turned off.
func (client *LDClient) BoolVariation(key string, user User, defaultVal bool) (bool, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(true))
	result, _ := value.(bool)
	return result, err
}

// IntVariation eturns the value of a feature flag (whose variations are integers) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) IntVariation(key string, user User, defaultVal int) (int, error) {
	value, err := client.variation(key, user, float64(defaultVal), reflect.TypeOf(float64(0)))
	result, _ := value.(float64)
	return int(result), err
}

// Float64Variation eturns the value of a feature flag (whose variations are floats) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) Float64Variation(key string, user User, defaultVal float64) (float64, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(float64(0)))
	result, _ := value.(float64)
	return result, err
}

// StringVariation eturns the value of a feature flag (whose variations are strings) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) StringVariation(key string, user User, defaultVal string) (string, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(string("string")))
	result, _ := value.(string)
	return result, err
}

// JsonVariation eturns the value of a feature flag (whose variations are JSON) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) JsonVariation(key string, user User, defaultVal json.RawMessage) (json.RawMessage, error) {
	if client.IsOffline() {
		return defaultVal, nil
	}
	value, index, flag, err := client.evaluateInternal(key, user, defaultVal)

	if err != nil {
		client.sendFlagRequestEvent(key, flag, user, index, defaultVal, defaultVal)
		return defaultVal, err
	}
	valueJsonRawMessage, err := ToJsonRawMessage(value)
	if err != nil {
		client.sendFlagRequestEvent(key, flag, user, index, defaultVal, defaultVal)
		return defaultVal, err
	}
	client.sendFlagRequestEvent(key, flag, user, index, valueJsonRawMessage, defaultVal)
	return valueJsonRawMessage, nil
}

// Generic method for evaluating a feature flag for a given user. The type of the returned interface{}
// will always be expectedType or the actual defaultValue will be returned.
func (client *LDClient) variation(key string, user User, defaultVal interface{}, expectedType reflect.Type) (interface{}, error) {
	if client.IsOffline() {
		return defaultVal, nil
	}
	value, index, flag, err := client.evaluateInternal(key, user, defaultVal)
	if err != nil {
		client.sendFlagRequestEvent(key, flag, user, index, defaultVal, defaultVal)
		return defaultVal, err
	}

	valueType := reflect.TypeOf(value)
	if expectedType != valueType {
		client.sendFlagRequestEvent(key, flag, user, index, defaultVal, defaultVal)
		return defaultVal, fmt.Errorf("Feature flag returned value: %+v of incompatible type: %+v; Expected: %+v", value, valueType, expectedType)
	}
	client.sendFlagRequestEvent(key, flag, user, index, value, defaultVal)
	return value, nil
}

func (client *LDClient) sendFlagRequestEvent(key string, flag *FeatureFlag, user User, variation *int, value, defaultVal interface{}) {
	if client.IsOffline() {
		return
	}
	evt := NewFeatureRequestEvent(key, flag, user, variation, value, defaultVal, nil)
	client.eventProcessor.SendEvent(evt)
}

// Evaluate returns the value of a feature for a specified user
func (client *LDClient) Evaluate(key string, user User, defaultVal interface{}) (interface{}, *int, error) {
	value, index, _, err := client.evaluateInternal(key, user, defaultVal)
	return value, index, err
}

func (client *LDClient) evaluateInternal(key string, user User, defaultVal interface{}) (interface{}, *int, *FeatureFlag, error) {
	if user.Key != nil && *user.Key == "" {
		client.config.Logger.Printf("WARN: User.Key is blank when evaluating flag: %s. Flag evaluation will proceed, but the user will not be stored in LaunchDarkly.", key)
	}

	var feature *FeatureFlag
	var storeErr error
	var ok bool

	if !client.Initialized() {
		if client.store.Initialized() {
			client.config.Logger.Printf("WARN: Feature flag evaluation called before LaunchDarkly client initialization completed; using last known values from feature store")
		} else {
			return defaultVal, nil, nil, ErrClientNotInitialized
		}
	}

	data, storeErr := client.store.Get(Features, key)

	if storeErr != nil {
		client.config.Logger.Printf("Encountered error fetching feature from store: %+v", storeErr)
		return defaultVal, nil, nil, storeErr
	}

	if data != nil {
		feature, ok = data.(*FeatureFlag)
		if !ok {
			return defaultVal, nil, nil, fmt.Errorf("unexpected data type (%T) found in store for feature key: %s. Returning default value", data, key)
		}
	} else {
		return defaultVal, nil, nil, fmt.Errorf("unknown feature key: %s Verify that this feature key exists. Returning default value", key)
	}

	if user.Key == nil {
		return defaultVal, nil, feature, fmt.Errorf("user.Key cannot be nil for user: %+v when evaluating flag: %s", user, key)
	}

	result, index, prereqEvents := client.evalFlag(*feature, user)
	for _, event := range prereqEvents {
		client.eventProcessor.SendEvent(event)
	}
	if result != nil {
		return result, index, feature, nil
	}
	return defaultVal, index, feature, nil
}
