package metrics

import (
	"context"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instruments holds the OTel metric instruments used for recording metrics.
type Instruments struct {
	connections         metric.Int64UpDownCounter // active connections (+1/-1)
	requestDuration     metric.Float64Histogram   // request duration in seconds
	eventsReceivedBytes metric.Int64Counter       // cumulative bytes of event data received
	eventsDropped       metric.Int64Counter       // cumulative count of events dropped due to capacity overflow
	eventsSent          metric.Int64Counter       // cumulative count of events successfully sent
	eventsFailedSend    metric.Int64Counter       // cumulative count of events that failed to send
	eventsBytesSent     metric.Int64Counter       // cumulative bytes of event payloads successfully sent
	pendingEvents       metric.Int64Gauge         // current number of events pending delivery
}

// Measure identifies what to record. Each pre-defined Measure var specifies which
// instruments should be incremented and what platform category to use.
type Measure struct {
	recordConnections bool
	recordDuration    bool
	recordPolling     bool
	platformCategory  string
}

// To avoid having to put nolint:gochecknoglobals on everything here, that linter is excluded
// specifically for this file in .golangci-lint.yml.
var (
	// BrowserConns is a Measure representing the current number of active stream connections from browsers.
	BrowserConns = Measure{recordConnections: true, platformCategory: BrowserPlatformCategory}

	// MobileConns is a Measure representing the current number of active stream connections from mobile SDKs.
	MobileConns = Measure{recordConnections: true, platformCategory: MobilePlatformCategory}

	// ServerConns is a Measure representing the current number of active stream connections from server-side SDKs.
	ServerConns = Measure{recordConnections: true, platformCategory: ServerPlatformCategory}

	// BrowserDuration is a Measure for recording request duration from browsers.
	BrowserDuration = Measure{recordDuration: true, platformCategory: BrowserPlatformCategory}

	// MobileDuration is a Measure for recording request duration from mobile SDKs.
	MobileDuration = Measure{recordDuration: true, platformCategory: MobilePlatformCategory}

	// ServerDuration is a Measure for recording request duration from server-side SDKs.
	ServerDuration = Measure{recordDuration: true, platformCategory: ServerPlatformCategory}

	// ServerPollingRequests is a Measure representing the total number of polling style requests received from server-side SDKs.
	ServerPollingRequests = Measure{recordPolling: true, platformCategory: ServerPlatformCategory}

	// MobilePollingRequests is a Measure representing the total number of polling requests from mobile SDKs.
	MobilePollingRequests = Measure{recordPolling: true, platformCategory: MobilePlatformCategory}

	// BrowserPollingRequests is a Measure representing the total number of polling requests from browser SDKs.
	BrowserPollingRequests = Measure{recordPolling: true, platformCategory: BrowserPlatformCategory}
)

// NewInstrumentsForTest creates Instruments backed by the given OTel meter.
// This is intended for use by tests outside the metrics package.
func NewInstrumentsForTest(meter metric.Meter) (*Instruments, error) {
	connections, err := meter.Int64UpDownCounter(connMeasureName)
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram(requestDurationMeasureName)
	if err != nil {
		return nil, err
	}
	eventsReceivedBytes, err := meter.Int64Counter(eventsReceivedMeasureName)
	if err != nil {
		return nil, err
	}
	eventsDropped, err := meter.Int64Counter(eventsDroppedMeasureName)
	if err != nil {
		return nil, err
	}
	eventsSent, err := meter.Int64Counter(eventsSentMeasureName)
	if err != nil {
		return nil, err
	}
	eventsFailedSend, err := meter.Int64Counter(eventsSendErrorsMeasureName)
	if err != nil {
		return nil, err
	}
	eventsBytesSent, err := meter.Int64Counter(eventsSentSizeMeasureName)
	if err != nil {
		return nil, err
	}
	pendingEvents, err := meter.Int64Gauge(eventsPendingMeasureName)
	if err != nil {
		return nil, err
	}
	return &Instruments{
		connections:         connections,
		requestDuration:     requestDuration,
		eventsReceivedBytes: eventsReceivedBytes,
		eventsDropped:       eventsDropped,
		eventsSent:          eventsSent,
		eventsFailedSend:    eventsFailedSend,
		eventsBytesSent:     eventsBytesSent,
		pendingEvents:       pendingEvents,
	}, nil
}

// RequestInfo contains per-request metadata used as metric attributes.
type RequestInfo struct {
	UserAgent          string
	SDKWrapper         string
	Route              string
	Method             string
	ApplicationID      string
	ApplicationVersion string
	InstanceID         string
	// Semconv fields populated after handler execution
	StatusCode      int
	URLScheme       string
	ProtocolVersion string
	ErrorType       string
}

func (ri RequestInfo) sanitized() (ua, wrapper, route, method, appID, appVersion, instanceID string) {
	return tracing.SanitizeAttributeValue(ri.UserAgent),
		tracing.SanitizeAttributeValue(ri.SDKWrapper),
		sanitizeRouteValue(ri.Route),
		tracing.SanitizeAttributeValue(ri.Method),
		tracing.SanitizeAttributeValue(ri.ApplicationID),
		tracing.SanitizeAttributeValue(ri.ApplicationVersion),
		tracing.SanitizeAttributeValue(ri.InstanceID)
}

// WithGauge increments the specified metric before running the function and then decrements it (for use with
// the active connection metrics).
func WithGauge(em *EnvironmentManager, instruments *Instruments, ri RequestInfo, f func(), measure Measure) {
	if em == nil || !measure.recordConnections {
		f()
		return
	}

	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildRequestAttributes(em.envKVs, measure.platformCategory, ua, wrapper, route, method, ri.URLScheme, appID, appVersion, instanceID)

	if instruments != nil {
		instruments.connections.Add(context.Background(), 1, metric.WithAttributeSet(attrs))
		defer instruments.connections.Add(context.Background(), -1, metric.WithAttributeSet(attrs))
	}

	if em.collector != nil {
		em.collector.RecordConnectionChange(measure.platformCategory, ua, wrapper, 1)
		defer em.collector.RecordConnectionChange(measure.platformCategory, ua, wrapper, -1)
	}

	f()
}

// WithCount runs a function and records polling metrics if applicable.
func WithCount(em *EnvironmentManager, ri RequestInfo, f func(), measure Measure) {
	if em != nil && measure.recordPolling && em.collector != nil {
		ua, wrapper, _, _, _, _, _ := ri.sanitized()
		em.collector.RecordPollingRequest(measure.platformCategory, ua, wrapper)
	}

	f()
}

// RecordEventsReceivedBytes records the number of event bytes received.
func RecordEventsReceivedBytes(ctx context.Context, instruments *Instruments, em *EnvironmentManager, platformCategory string, ri RequestInfo, bytes int64) {
	if em == nil || instruments == nil || bytes <= 0 {
		return
	}
	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildRequestAttributes(em.envKVs, platformCategory, ua, wrapper, route, method, ri.URLScheme, appID, appVersion, instanceID)
	instruments.eventsReceivedBytes.Add(ctx, bytes, metric.WithAttributeSet(attrs))
}

// RecordRequestDuration records a request duration measurement with the given attributes.
// Duration is recorded in seconds per OTEL HTTP semantic conventions.
func RecordRequestDuration(ctx context.Context, instruments *Instruments, em *EnvironmentManager, ri RequestInfo, duration time.Duration, measure Measure) {
	if em == nil || instruments == nil || !measure.recordDuration {
		return
	}
	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildDurationAttributes(em.envKVs, measure.platformCategory, ua, wrapper, route, method, appID, appVersion, instanceID, ri.URLScheme, ri.ProtocolVersion, ri.ErrorType, ri.StatusCode)
	instruments.requestDuration.Record(ctx, duration.Seconds(), metric.WithAttributeSet(attrs))
}

// EventMetricsRecorder implements the EventMetrics interface defined in both the events package
// and go-sdk-events, recording event processing metrics via OTEL instruments. It uses
// environment-level attributes only (relayId, env) since event drops occur asynchronously,
// detached from any specific HTTP request context.
type EventMetricsRecorder struct {
	instruments *Instruments
	envKVs      []attribute.KeyValue // private copy, safe for concurrent read
	envAttrs    attribute.Set        // pre-computed to avoid concurrent sort in attribute.NewSet
}

// RecordDroppedEvents records the number of events dropped due to capacity overflow.
func (r *EventMetricsRecorder) RecordDroppedEvents(count int) {
	if r.instruments == nil || count <= 0 {
		return
	}
	r.instruments.eventsDropped.Add(context.Background(), int64(count), metric.WithAttributeSet(r.envAttrs))
}

// RecordEventsSent records the number of events successfully delivered to the events service.
func (r *EventMetricsRecorder) RecordEventsSent(count int) {
	if r.instruments == nil || count <= 0 {
		return
	}
	r.instruments.eventsSent.Add(context.Background(), int64(count), metric.WithAttributeSet(r.envAttrs))
}

// RecordPendingEvents records the current number of events pending delivery.
func (r *EventMetricsRecorder) RecordPendingEvents(depth int) {
	if r.instruments == nil {
		return
	}
	r.instruments.pendingEvents.Record(context.Background(), int64(depth), metric.WithAttributeSet(r.envAttrs))
}

// RecordEventsBytesSent records the size of event payloads successfully delivered.
func (r *EventMetricsRecorder) RecordEventsBytesSent(bytes int) {
	if r.instruments == nil || bytes <= 0 {
		return
	}
	r.instruments.eventsBytesSent.Add(context.Background(), int64(bytes), metric.WithAttributeSet(r.envAttrs))
}

// RecordEventsFailedSend records the number of events that could not be delivered after all retries.
// The status code from the metadata is included as an attribute on the metric.
func (r *EventMetricsRecorder) RecordEventsFailedSend(count int, metadata ldevents.EventSendFailureMetadata) {
	if r.instruments == nil || count <= 0 {
		return
	}
	kvs := make([]attribute.KeyValue, len(r.envKVs), len(r.envKVs)+1)
	copy(kvs, r.envKVs)
	kvs = append(kvs, statusCodeAttrKey.Int(metadata.StatusCode))
	attrs := attribute.NewSet(kvs...)
	r.instruments.eventsFailedSend.Add(context.Background(), int64(count), metric.WithAttributeSet(attrs))
}
