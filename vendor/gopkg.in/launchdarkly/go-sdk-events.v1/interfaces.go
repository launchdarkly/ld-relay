package ldevents

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

// EventProcessor defines the interface for dispatching analytics events.
type EventProcessor interface {
	// SendEvent records an event asynchronously.
	SendEvent(Event)
	// Flush specifies that any buffered events should be sent as soon as possible, rather than waiting
	// for the next flush interval. This method is asynchronous, so events still may not be sent
	// until a later time.
	Flush()
	// Close shuts down all event processor activity, after first ensuring that all events have been
	// delivered. Subsequent calls to SendEvent() or Flush() will be ignored.
	Close() error
}

// EventSender defines the interface for delivering already-formatted analytics event data to the events service.
type EventSender interface {
	// SendEventData attempts to deliver an event data payload.
	SendEventData(kind EventDataKind, data []byte, eventCount int) EventSenderResult
}

// EventDataKind is a parameter passed to EventSender to indicate the type of event data payload.
type EventDataKind string

const (
	// AnalyticsEventDataKind denotes a payload of analytics event data.
	AnalyticsEventDataKind EventDataKind = "analytics"
	// DiagnosticEventDataKind denotes a payload of diagnostic event data.
	DiagnosticEventDataKind EventDataKind = "diagnostic"
)

// EventSenderResult is the return type for EventSender.SendEventData.
type EventSenderResult struct {
	// Success is true if the event payload was delivered.
	Success bool
	// MustShutDown is true if the server returned an error indicating that no further event data should be sent.
	// This normally means that the SDK key is invalid.
	MustShutDown bool
	// TimeFromServer is the last known date/time reported by the server, if available, otherwise zero.
	TimeFromServer ldtime.UnixMillisecondTime
}

// FlagEventProperties is an interface that provides the basic information about a feature flag that the events
// package needs, without having a specific dependency on the server-side data model. An implementation of this
// interface for server-side feature flags is provided in go-server-sdk-evaluation; if we ever create a
// client-side Go SDK, that will have its own implementation.
type FlagEventProperties interface {
	// GetKey returns the feature flag key.
	GetKey() string
	// GetVersion returns the feature flag version.
	GetVersion() int
	// IsFullEventTrackingEnabled returns true if the flag has been configured to always generate detailed event data.
	IsFullEventTrackingEnabled() bool
	// GetDebugEventsUntilDate returns zero normally, but if event debugging has been temporarily enabled for the flag,
	// it returns the time at which debugging mode should expire.
	GetDebugEventsUntilDate() ldtime.UnixMillisecondTime
	// IsExperimentationEnabled returns true if, based on the EvaluationReason returned by the flag evaluation,
	// an event for that evaluation should have full tracking enabled and always report the reason even if the
	// application didn't explicitly request this. For instance, this is true if a rule was matched that had
	// tracking enabled for that specific rule.
	//
	// This differs from IsFullEventTrackingEnabled() in that it is dependent on the result of a specific
	// evaluation; also, IsFullEventTrackingEnabled() being true does not imply that the event should always
	// contain a reason, whereas IsExperimentationEnabled() being true does force the reason to be included.
	IsExperimentationEnabled(reason ldreason.EvaluationReason) bool
}
