package ldclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"time"
)

const Version string = "0.0.3"

// The LaunchDarkly client. Client instances are thread-safe.
// Applications should instantiate a single instance for the lifetime
// of their application.
type LDClient struct {
	apiKey          string
	config          Config
	eventProcessor  *eventProcessor
	updateProcessor updateProcessor
	store           FeatureStore
}

// Exposes advanced configuration options for the LaunchDarkly client.
type Config struct {
	BaseUri          string
	StreamUri        string
	EventsUri        string
	Capacity         int
	FlushInterval    time.Duration
	SamplingInterval int32
	PollInterval     time.Duration
	Logger           *log.Logger
	Timeout          time.Duration
	Stream           bool
	FeatureStore     FeatureStore
	UseLdd           bool
	SendEvents       bool
	Offline          bool
}

type updateProcessor interface {
	initialized() bool
	close()
	start(chan<- bool)
}

// Provides the default configuration options for the LaunchDarkly client.
// The easiest way to create a custom configuration is to start with the
// default config, and set the custom options from there. For example:
//   var config = DefaultConfig
//   config.Capacity = 2000
var DefaultConfig = Config{
	BaseUri:       "https://app.launchdarkly.com",
	StreamUri:     "https://stream.launchdarkly.com",
	EventsUri:     "https://events.launchdarkly.com",
	Capacity:      1000,
	FlushInterval: 5 * time.Second,
	PollInterval:  1 * time.Second,
	Logger:        log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags),
	Timeout:       3000 * time.Millisecond,
	Stream:        true,
	FeatureStore:  nil,
	UseLdd:        false,
	SendEvents:    true,
	Offline:       false,
}

var ErrInitializationTimeout = errors.New("Timeout encountered waiting for LaunchDarkly client initialization")
var ErrClientNotInitialized = errors.New("Toggle called before LaunchDarkly client initialization completed")

// Creates a new client instance that connects to LaunchDarkly with the default configuration. In most
// cases, you should use this method to instantiate your client. The optional duration parameter allows callers to
// block until the client has connected to LaunchDarkly and is properly initialized.
func MakeClient(apiKey string, waitFor time.Duration) (*LDClient, error) {
	return MakeCustomClient(apiKey, DefaultConfig, waitFor)
}

// Creates a new client instance that connects to LaunchDarkly with a custom configuration. The optional duration parameter allows callers to
// block until the client has connected to LaunchDarkly and is properly initialized.
func MakeCustomClient(apiKey string, config Config, waitFor time.Duration) (*LDClient, error) {
	var updateProcessor updateProcessor
	var store FeatureStore

	ch := make(chan bool)

	config.BaseUri = strings.TrimRight(config.BaseUri, "/")
	config.EventsUri = strings.TrimRight(config.EventsUri, "/")
	if config.PollInterval < (1 * time.Second) {
		config.PollInterval = 1 * time.Second
	}

	requestor := newRequestor(apiKey, config)

	if config.FeatureStore == nil {
		config.FeatureStore = NewInMemoryFeatureStore()
	}

	store = config.FeatureStore

	if !config.UseLdd && !config.Offline {
		if config.Stream {
			updateProcessor = newStreamProcessor(apiKey, config, requestor)
		} else {
			updateProcessor = newPollingProcessor(config, requestor)
		}
		updateProcessor.start(ch)
	}

	client := LDClient{
		apiKey:          apiKey,
		config:          config,
		eventProcessor:  newEventProcessor(apiKey, config),
		updateProcessor: updateProcessor,
		store:           store,
	}

	if config.UseLdd || config.Offline {
		return &client, nil
	}

	timeout := time.After(waitFor)

	for {
		select {
		case <-ch:
			return &client, nil
		case <-timeout:
			if waitFor > 0 {
				return &client, ErrInitializationTimeout
			}
			return &client, nil
		}
	}
}

func (client *LDClient) Identify(user User) error {
	if client.IsOffline() {
		return nil
	}
	evt := NewIdentifyEvent(user)
	return client.eventProcessor.sendEvent(evt)
}

// Tracks that a user has performed an event. Custom data can be attached to the
// event, and is serialized to JSON using the encoding/json package (http://golang.org/pkg/encoding/json/).
func (client *LDClient) Track(key string, user User, data interface{}) error {
	if client.IsOffline() {
		return nil
	}
	evt := NewCustomEvent(key, user, data)
	return client.eventProcessor.sendEvent(evt)
}

// Returns whether the LaunchDarkly client is in offline mode.
func (client *LDClient) IsOffline() bool {
	return client.config.Offline
}

// Returns whether the LaunchDarkly client is initialized.
func (client *LDClient) Initialized() bool {
	return client.IsOffline() || client.config.UseLdd || client.updateProcessor.initialized()
}

// Shuts down the LaunchDarkly client. After calling this, the LaunchDarkly client
// should no longer be used.
func (client *LDClient) Close() {
	if client.IsOffline() {
		return
	}
	client.eventProcessor.close()
	if !client.config.UseLdd {
		client.updateProcessor.close()
	}
}

// Immediately flushes queued events.
func (client *LDClient) Flush() {
	if client.IsOffline() {
		return
	}
	client.eventProcessor.flush()
}

// Returns the value of a boolean feature flag for a given user. Returns defaultVal if
// there is an error, if the flag doesn't exist, the client hasn't completed initialization,
// or the feature is turned off.
func (client *LDClient) Toggle(key string, user User, defaultVal bool) (bool, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(bool(true)))
	result, _ := value.(bool)
	return result, err
}

// Returns the value of a feature flag (whose variations are integers) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) IntVariation(key string, user User, defaultVal int) (int, error) {
	value, err := client.variation(key, user, float64(defaultVal), reflect.TypeOf(float64(0)))
	result, _ := value.(float64)
	return int(result), err
}

// Returns the value of a feature flag (whose variations are floats) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) Float64Variation(key string, user User, defaultVal float64) (float64, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(float64(0)))
	result, _ := value.(float64)
	return result, err
}

// Returns the value of a feature flag (whose variations are strings) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) StringVariation(key string, user User, defaultVal string) (string, error) {
	value, err := client.variation(key, user, defaultVal, reflect.TypeOf(string("string")))
	result, _ := value.(string)
	return result, err
}

// Returns the value of a feature flag (whose variations are JSON) for the given user.
// Returns defaultVal if there is an error, if the flag doesn't exist, or the feature is turned off.
func (client *LDClient) JsonVariation(key string, user User, defaultVal json.RawMessage) (json.RawMessage, error) {
	if client.IsOffline() {
		return defaultVal, nil
	}
	value, err := client.Evaluate(key, user, defaultVal)

	if err != nil {
		client.sendFlagRequestEvent(key, user, defaultVal, defaultVal)
		return defaultVal, err
	}
	valueJsonRawMessage, err := ToJsonRawMessage(value)
	if err != nil {
		client.sendFlagRequestEvent(key, user, defaultVal, defaultVal)
		return defaultVal, err
	}
	client.sendFlagRequestEvent(key, user, valueJsonRawMessage, defaultVal)
	return valueJsonRawMessage, nil
}

// Generic method for evaluating a feature flag for a given user. The type of the returned interface{}
// will always be expectedType or the actual defaultValue will be returned.
func (client *LDClient) variation(key string, user User, defaultVal interface{}, expectedType reflect.Type) (interface{}, error) {
	if client.IsOffline() {
		return defaultVal, nil
	}
	value, err := client.Evaluate(key, user, defaultVal)
	if err != nil {
		client.sendFlagRequestEvent(key, user, defaultVal, defaultVal)
		return defaultVal, err
	}

	valueType := reflect.TypeOf(value)
	if expectedType != valueType {
		client.sendFlagRequestEvent(key, user, defaultVal, defaultVal)
		return defaultVal, fmt.Errorf("Feature flag returned value: %+v of incompatible type: %+v; Expected: %+v", value, valueType, expectedType)
	}
	client.sendFlagRequestEvent(key, user, value, defaultVal)
	return value, nil
}

func (client *LDClient) sendFlagRequestEvent(key string, user User, value, defaultVal interface{}) error {
	if client.IsOffline() {
		return nil
	}
	evt := NewFeatureRequestEvent(key, user, value, defaultVal)
	return client.eventProcessor.sendEvent(evt)
}

func (client *LDClient) Evaluate(key string, user User, defaultVal interface{}) (interface{}, error) {
	if user.Key == nil {
		return defaultVal, fmt.Errorf("User.Key cannot be nil for user: %+v", user)
	}
	var feature FeatureFlag
	var storeErr error
	var featurePtr *FeatureFlag

	if !client.Initialized() {
		return defaultVal, ErrClientNotInitialized
	}

	featurePtr, storeErr = client.store.Get(key)

	if storeErr != nil {
		client.config.Logger.Printf("Encountered error fetching feature from store: %+v", storeErr)
		return defaultVal, storeErr
	}

	if featurePtr != nil {
		feature = *featurePtr
	} else {
		return defaultVal, fmt.Errorf("Unknown feature key: %s Verify that this feature key exists. Returning default value.", key)
	}

	if feature.On {
		evalResult, err := feature.EvaluateExplain(user, client.store)
		if err != nil {
			return defaultVal, err
		}
		if !client.IsOffline() {
			for _, event := range evalResult.PrerequisiteRequestEvents {
				err := client.eventProcessor.sendEvent(event)
				if err != nil {
					client.config.Logger.Printf("WARN: Error sending feature request event to LaunchDarkly: %+v", err)
				}
			}
		}

		if evalResult.Value != nil {
			return evalResult.Value, nil
		}
		// If the value is nil, but the error is not, fall through and use the off variation
	}

	if feature.OffVariation != nil && *feature.OffVariation < len(feature.Variations) {
		value := feature.Variations[*feature.OffVariation]
		return value, nil
	}
	return defaultVal, nil
}
