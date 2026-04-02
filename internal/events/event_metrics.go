package events

import (
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
)

// EventSendFailureMetadata is an alias for ldevents.EventSendFailureMetadata so that the
// ld-relay EventMetrics interface is structurally identical to the go-sdk-events one.
// This allows a single concrete type to satisfy both interfaces.
type EventSendFailureMetadata = ldevents.EventSendFailureMetadata

// EventMetrics defines an interface for receiving metrics about event processing. This is used by
// the metrics package to record telemetry (e.g. via OpenTelemetry) about events that are dropped,
// sent, or otherwise processed by the relay's event forwarding pipeline.
//
// This interface mirrors the one in go-sdk-events. The metrics package provides a single concrete
// implementation that satisfies both via Go structural typing.
type EventMetrics interface {
	// RecordDroppedEvents is called when events are discarded because the event buffer has reached
	// its configured capacity. The count parameter indicates how many events were dropped.
	RecordDroppedEvents(count int)

	// RecordEventsSent is called when a batch of events has been successfully delivered to the
	// events service. The count parameter indicates how many events were in the batch.
	RecordEventsSent(count int)

	// RecordEventsFailedSend is called when a batch of events could not be delivered to the
	// events service after all retry attempts. The count parameter indicates how many events
	// were in the failed batch. The metadata parameter provides additional context about the failure.
	RecordEventsFailedSend(count int, metadata EventSendFailureMetadata)

	// RecordEventsBytesSent is called when a batch of events has been successfully delivered.
	// The bytes parameter is the size of the serialized event payload before compression.
	RecordEventsBytesSent(bytes int)

	// RecordPendingEvents is called after any operation that changes the number of events
	// buffered awaiting delivery. The count parameter is the current total number of events pending.
	RecordPendingEvents(count int)
}

// NoOpEventMetrics is a default implementation of EventMetrics that does nothing.
// It is used when no EventMetrics is provided, eliminating the need for nil checks
// at every call site.
type NoOpEventMetrics struct{}

func (NoOpEventMetrics) RecordDroppedEvents(int)                              {}
func (NoOpEventMetrics) RecordEventsSent(int)                                 {}
func (NoOpEventMetrics) RecordEventsFailedSend(int, EventSendFailureMetadata) {}
func (NoOpEventMetrics) RecordEventsBytesSent(int)                            {}
func (NoOpEventMetrics) RecordPendingEvents(int)                              {}
