package ldclient

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// Config exposes advanced configuration options for the LaunchDarkly client.
type Config struct {
	// The base URI of the main LaunchDarkly service. This should not normally be changed except for testing.
	BaseUri string
	// The base URI of the LaunchDarkly streaming service. This should not normally be changed except for testing.
	StreamUri string
	// The base URI of the LaunchDarkly service that accepts analytics events. This should not normally be
	// changed except for testing.
	EventsUri string
	// The full URI for posting analytics events. This is different from EventsUri in that the client will not
	// add the default URI path to it. It should not normally be changed except for testing, and if set, it
	// causes EventsUri to be ignored.
	EventsEndpointUri string
	// The capacity of the events buffer. The client buffers up to this many events in memory before flushing.
	// If the capacity is exceeded before the buffer is flushed, events will be discarded.
	Capacity int
	// The time between flushes of the event buffer. Decreasing the flush interval means that the event buffer
	// is less likely to reach capacity.
	FlushInterval time.Duration
	// Enables event sampling if non-zero. When set to the default of zero, all events are sent to Launchdarkly.
	// If greater than zero, there is a 1 in SamplingInterval chance that events will be sent (for example, a
	// value of 20 means on average 5% of events will be sent).
	SamplingInterval int32
	// The polling interval (when streaming is disabled). Values less than the default of MinimumPollInterval
	// will be set to the default.
	PollInterval time.Duration
	// An object that can be used to produce log output.
	Logger Logger
	// The connection timeout to use when making polling requests to LaunchDarkly.
	Timeout time.Duration
	// Sets the implementation of FeatureStore for holding feature flags and related data received from
	// LaunchDarkly. See NewInMemoryFeatureStore (the default) and the redis, ldconsul, and lddynamodb packages.
	FeatureStore FeatureStore
	// Sets whether streaming mode should be enabled. By default, streaming is enabled. It should only be
	// disabled on the advice of LaunchDarkly support.
	Stream bool
	// Sets whether this client should use the LaunchDarkly relay in daemon mode. In this mode, the client does
	// not subscribe to the streaming or polling API, but reads data only from the feature store. See:
	// https://docs.launchdarkly.com/docs/the-relay-proxy
	UseLdd bool
	// Sets whether to send analytics events back to LaunchDarkly. By default, the client will send events. This
	// differs from Offline in that it only affects sending events, not streaming or polling for events from the
	// server.
	SendEvents bool
	// Sets whether this client is offline. An offline client will not make any network connections to LaunchDarkly,
	// and will return default values for all feature flags.
	Offline bool
	// Sets whether or not all user attributes (other than the key) should be hidden from LaunchDarkly. If this
	// is true, all user attribute values will be private, not just the attributes specified in PrivateAttributeNames.
	AllAttributesPrivate bool
	// Set to true if you need to see the full user details in every analytics event.
	InlineUsersInEvents bool
	// Marks a set of user attribute names private. Any users sent to LaunchDarkly with this configuration
	// active will have attributes with these names removed.
	PrivateAttributeNames []string
	// Deprecated. Please use UpdateProcessorFactory.
	UpdateProcessor UpdateProcessor
	// Factory to create an object that is responsible for receiving feature flag updates from LaunchDarkly.
	// If nil, a default implementation will be used depending on the rest of the configuration
	// (streaming, polling, etc.); a custom implementation can be substituted for testing.
	UpdateProcessorFactory UpdateProcessorFactory
	// An object that is responsible for recording or sending analytics events. If nil, a
	// default implementation will be used; a custom implementation can be substituted for testing.
	EventProcessor EventProcessor
	// The number of user keys that the event processor can remember at any one time, so that
	// duplicate user details will not be sent in analytics events.
	UserKeysCapacity int
	// The interval at which the event processor will reset its set of known user keys.
	UserKeysFlushInterval time.Duration
	UserAgent             string
	// If not nil, this function will be called to create an HTTP client instead of using the default
	// client. The SDK may modify the client properties after that point (for instance, to add caching),
	// but will not replace the underlying Transport, and will not modify any timeout properties you set.
	HTTPClientFactory func(Config) http.Client
}

// UpdateProcessorFactory is a function that creates an UpdateProcessor.
type UpdateProcessorFactory func(sdkKey string, config Config) (UpdateProcessor, error)

// MinimumPollInterval describes the minimum value for Config.PollInterval. If you specify a smaller interval,
// the minimum will be used instead.
const MinimumPollInterval = 30 * time.Second

func (c Config) newHTTPClient() *http.Client {
	if c.HTTPClientFactory != nil {
		client := c.HTTPClientFactory(c)
		return &client
	}
	dialer := net.Dialer{
		KeepAlive: 1 * time.Minute,
	}
	client := http.Client{
		Timeout: c.Timeout,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}
	return &client
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
	Capacity:              10000,
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
