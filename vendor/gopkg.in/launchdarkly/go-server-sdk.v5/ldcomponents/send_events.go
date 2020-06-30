package ldcomponents

import (
	"strings"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
)

const (
	// DefaultEventsBaseURI is the default value for EventProcessorBuilder.BaseURI.
	DefaultEventsBaseURI = "https://events.launchdarkly.com"
	// DefaultEventsCapacity is the default value for EventProcessorBuilder.Capacity.
	DefaultEventsCapacity = 10000
	// DefaultDiagnosticRecordingInterval is the default value for EventProcessorBuilder.DiagnosticRecordingInterval.
	DefaultDiagnosticRecordingInterval = 15 * time.Minute
	// DefaultFlushInterval is the default value for EventProcessorBuilder.FlushInterval.
	DefaultFlushInterval = 5 * time.Second
	// DefaultUserKeysCapacity is the default value for EventProcessorBuilder.UserKeysCapacity.
	DefaultUserKeysCapacity = 1000
	// DefaultUserKeysFlushInterval is the default value for EventProcessorBuilder.UserKeysFlushInterval.
	DefaultUserKeysFlushInterval = 5 * time.Minute
	// MinimumDiagnosticRecordingInterval is the minimum value for EventProcessorBuilder.DiagnosticRecordingInterval.
	MinimumDiagnosticRecordingInterval = 60 * time.Second
)

// EventProcessorBuilder provides methods for configuring analytics event behavior.
//
// See SendEvents for usage.
type EventProcessorBuilder struct {
	allAttributesPrivate        bool
	baseURI                     string
	capacity                    int
	diagnosticRecordingInterval time.Duration
	flushInterval               time.Duration
	inlineUsersInEvents         bool
	logUserKeyInErrors          bool
	privateAttributeNames       []lduser.UserAttribute
	userKeysCapacity            int
	userKeysFlushInterval       time.Duration
}

// SendEvents returns a configuration builder for analytics event delivery.
//
// The default configuration has events enabled with default settings. If you want to customize this
// behavior, call this method to obtain a builder, change its properties with the EventProcessorBuilder
// methods, and store it in Config.Events:
//
//     config := ld.Config{
//         Events: ldcomponents.SendEvents().Capacity(5000).FlushInterval(2 * time.Second),
//     }
//
// To disable analytics events, use NoEvents instead of SendEvents.
func SendEvents() *EventProcessorBuilder {
	return &EventProcessorBuilder{
		baseURI:                     DefaultEventsBaseURI,
		capacity:                    DefaultEventsCapacity,
		diagnosticRecordingInterval: DefaultDiagnosticRecordingInterval,
		flushInterval:               DefaultFlushInterval,
		userKeysCapacity:            DefaultUserKeysCapacity,
		userKeysFlushInterval:       DefaultUserKeysFlushInterval,
	}
}

// CreateEventProcessor is called by the SDK to create the event processor instance.
func (b *EventProcessorBuilder) CreateEventProcessor(
	context interfaces.ClientContext,
) (ldevents.EventProcessor, error) {
	loggers := context.GetLogging().GetLoggers()
	eventSender := ldevents.NewServerSideEventSender(context.GetHTTP().CreateHTTPClient(),
		context.GetBasic().SDKKey, b.baseURI, context.GetHTTP().GetDefaultHeaders(), loggers)
	eventsConfig := ldevents.EventsConfiguration{
		AllAttributesPrivate:        b.allAttributesPrivate,
		Capacity:                    b.capacity,
		DiagnosticRecordingInterval: b.diagnosticRecordingInterval,
		EventSender:                 eventSender,
		FlushInterval:               b.flushInterval,
		InlineUsersInEvents:         b.inlineUsersInEvents,
		Loggers:                     loggers,
		LogUserKeyInErrors:          b.logUserKeyInErrors,
		PrivateAttributeNames:       b.privateAttributeNames,
		UserKeysCapacity:            b.userKeysCapacity,
		UserKeysFlushInterval:       b.userKeysFlushInterval,
	}
	if hdm, ok := context.(internal.HasDiagnosticsManager); ok {
		eventsConfig.DiagnosticsManager = hdm.GetDiagnosticsManager()
	}
	return ldevents.NewDefaultEventProcessor(eventsConfig), nil
}

// AllAttributesPrivate sets  or not all optional user attributes should be hidden from LaunchDarkly.
//
// If this is true, all user attribute values (other than the key) will be private, not just the attributes
// specified with PrivateAttributeNames or on a per-user basis with UserBuilder methods. By default, it is false.
func (b *EventProcessorBuilder) AllAttributesPrivate(value bool) *EventProcessorBuilder {
	b.allAttributesPrivate = value
	return b
}

// BaseURI sets a custom base URI for the events service.
//
// You will only need to change this value in the following cases:
//
// 1. You are using the Relay Proxy (https://docs.launchdarkly.com/docs/the-relay-proxy). Set BaseURI to the base URI of
// the Relay Proxy instance.
//
// 2. You are connecting to a test server or anything else other than the standard LaunchDarkly service.
func (b *EventProcessorBuilder) BaseURI(baseURI string) *EventProcessorBuilder {
	if baseURI == "" {
		b.baseURI = DefaultEventsBaseURI
	} else {
		b.baseURI = strings.TrimRight(baseURI, "/")
	}
	return b
}

// Capacity sets the capacity of the events buffer.
//
// The client buffers up to this many events in memory before flushing. If the capacity is exceeded before
// the buffer is flushed (see FlushInterval), events will be discarded. Increasing the capacity means that
// events are less likely to be discarded, at the cost of consuming more memory.
//
// The default value is DefaultEventsCapacity.
func (b *EventProcessorBuilder) Capacity(capacity int) *EventProcessorBuilder {
	b.capacity = capacity
	return b
}

// DiagnosticRecordingInterval sets the interval at which periodic diagnostic data is sent.
//
// The default value is DefaultDiagnosticRecordingInterval; the minimum value is MinimumDiagnosticRecordingInterval.
// This property is ignored if Config.DiagnosticOptOut is set to true.
func (b *EventProcessorBuilder) DiagnosticRecordingInterval(interval time.Duration) *EventProcessorBuilder {
	if interval < MinimumDiagnosticRecordingInterval {
		b.diagnosticRecordingInterval = MinimumDiagnosticRecordingInterval
	} else {
		b.diagnosticRecordingInterval = interval
	}
	return b
}

// FlushInterval sets the interval between flushes of the event buffer.
//
// Decreasing the flush interval means that the event buffer is less likely to reach capacity (see Capacity).
//
// The default value is DefaultFlushInterval.
func (b *EventProcessorBuilder) FlushInterval(interval time.Duration) *EventProcessorBuilder {
	b.flushInterval = interval
	return b
}

// InlineUsersInEvents sets whether to include full user details in every analytics event.
//
// The default is false: events will only include the user key, except for one "index" event that provides
// the full details for the user.
func (b *EventProcessorBuilder) InlineUsersInEvents(value bool) *EventProcessorBuilder {
	b.inlineUsersInEvents = value
	return b
}

// PrivateAttributeNames marks a set of attribute names as always private.
//
// Any users sent to LaunchDarkly with this configuration active will have attributes with these
// names removed. This is in addition to any attributes that were marked as private for an
// individual user with UserBuilder methods. Setting AllAttributePrivate to true overrides this.
//
//     config := ld.Config{
//         Events: ldcomponents.SendEvents().
//             PrivateAttributeNames(lduser.EmailAttribute, lduser.UserAttribute("some-custom-attribute")),
//     }
func (b *EventProcessorBuilder) PrivateAttributeNames(attributes ...lduser.UserAttribute) *EventProcessorBuilder {
	b.privateAttributeNames = attributes
	return b
}

// UserKeysCapacity sets the number of user keys that the event processor can remember at any one time.
//
// To avoid sending duplicate user details in analytics events, the SDK maintains a cache of recently
// seen user keys, expiring at an interval set by UserKeysFlushInterval.
//
// The default value is DefaultUserKeysCapacity.
func (b *EventProcessorBuilder) UserKeysCapacity(userKeysCapacity int) *EventProcessorBuilder {
	b.userKeysCapacity = userKeysCapacity
	return b
}

// UserKeysFlushInterval sets the interval at which the event processor will reset its cache of known user keys.
//
// The default value is DefaultUserKeysFlushInterval.
func (b *EventProcessorBuilder) UserKeysFlushInterval(interval time.Duration) *EventProcessorBuilder {
	b.userKeysFlushInterval = interval
	return b
}

// DescribeConfiguration is used internally by the SDK to inspect the configuration.
func (b *EventProcessorBuilder) DescribeConfiguration() ldvalue.Value {
	return ldvalue.ObjectBuild().
		Set("allAttributesPrivate", ldvalue.Bool(b.allAttributesPrivate)).
		Set("customEventsURI", ldvalue.Bool(b.baseURI != "" && b.baseURI != DefaultEventsBaseURI)).
		Set("diagnosticRecordingIntervalMillis", durationToMillisValue(b.diagnosticRecordingInterval)).
		Set("eventsCapacity", ldvalue.Int(b.capacity)).
		Set("eventsFlushIntervalMillis", durationToMillisValue(b.flushInterval)).
		Set("inlineUsersInEvents", ldvalue.Bool(b.inlineUsersInEvents)).
		Set("userKeysCapacity", ldvalue.Int(b.userKeysCapacity)).
		Set("userKeysFlushIntervalMillis", durationToMillisValue(b.userKeysFlushInterval)).
		Build()
}

func durationToMillisValue(d time.Duration) ldvalue.Value {
	return ldvalue.Float64(float64(uint64(d / time.Millisecond)))
}
