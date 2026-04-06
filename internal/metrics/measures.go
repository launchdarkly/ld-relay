package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// Instruments holds the OTel metric instruments used for recording metrics.
type Instruments struct {
	connections         metric.Int64UpDownCounter // active connections (+1/-1)
	requests            metric.Int64Counter       // cumulative HTTP requests
	requestDuration     metric.Float64Histogram   // request duration in seconds
	eventsIngestedBytes metric.Int64Counter       // cumulative bytes of event data ingested
}

// Measure identifies what to record. Each pre-defined Measure var specifies which
// instruments should be incremented and what platform category to use.
type Measure struct {
	recordConnections bool
	recordRequests    bool
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

	// BrowserRequests is a Measure representing the number of HTTP requests from browsers.
	BrowserRequests = Measure{recordRequests: true, platformCategory: BrowserPlatformCategory}

	// MobileRequests is a Measure representing the number of HTTP requests from mobile SDKs.
	MobileRequests = Measure{recordRequests: true, platformCategory: MobilePlatformCategory}

	// ServerRequests is a Measure representing the number of HTTP requests from server-side SDKs.
	ServerRequests = Measure{recordRequests: true, platformCategory: ServerPlatformCategory}

	// PollingRequests is a Measure representing the total number of polling style requests received from server-side SDKs.
	PollingRequests = Measure{recordPolling: true, platformCategory: ServerPlatformCategory}
)

// NewInstrumentsForTest creates Instruments backed by the given OTel meter.
// This is intended for use by tests outside the metrics package.
func NewInstrumentsForTest(meter metric.Meter) (*Instruments, error) {
	connections, err := meter.Int64UpDownCounter(connMeasureName)
	if err != nil {
		return nil, err
	}
	requests, err := meter.Int64Counter(requestMeasureName)
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram(requestDurationMeasureName)
	if err != nil {
		return nil, err
	}
	eventsIngestedBytes, err := meter.Int64Counter(eventsIngestedBytesMeasureName)
	if err != nil {
		return nil, err
	}
	return &Instruments{
		connections:         connections,
		requests:            requests,
		requestDuration:     requestDuration,
		eventsIngestedBytes: eventsIngestedBytes,
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
}

func (ri RequestInfo) sanitized() (ua, wrapper, route, method, appID, appVersion, instanceID string) {
	return sanitizeTagValue(ri.UserAgent),
		sanitizeTagValue(ri.SDKWrapper),
		sanitizeRouteValue(ri.Route),
		sanitizeTagValue(ri.Method),
		sanitizeTagValue(ri.ApplicationID),
		sanitizeTagValue(ri.ApplicationVersion),
		sanitizeTagValue(ri.InstanceID)
}

// WithGauge increments the specified metric before running the function and then decrements it (for use with
// the active connection metrics).
func WithGauge(em *EnvironmentManager, instruments *Instruments, ri RequestInfo, f func(), measure Measure) {
	if em == nil || !measure.recordConnections {
		f()
		return
	}

	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildRequestAttributes(em.envKVs, measure.platformCategory, ua, wrapper, route, method, appID, appVersion, instanceID)

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

// WithCount runs a function and records a single-unit increment for the specified metric.
func WithCount(em *EnvironmentManager, instruments *Instruments, ri RequestInfo, f func(), measure Measure) {
	if em == nil {
		f()
		return
	}

	ua, wrapper, _, _, _, _, _ := ri.sanitized()
	attrs := buildAttributes(em.envKVs, measure.platformCategory, ua, wrapper)

	if measure.recordRequests && instruments != nil {
		instruments.requests.Add(context.Background(), 1, metric.WithAttributeSet(attrs))
	}
	if measure.recordPolling && em.collector != nil {
		em.collector.RecordPollingRequest(measure.platformCategory, ua, wrapper)
	}

	f()
}

// WithRouteCount records a route hit for the specified metric.
func WithRouteCount(ctx context.Context, em *EnvironmentManager, instruments *Instruments, ri RequestInfo, f func(), measure Measure) {
	if em != nil && instruments != nil && measure.recordRequests {
		ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
		attrs := buildRequestAttributes(em.envKVs, measure.platformCategory, ua, wrapper, route, method, appID, appVersion, instanceID)
		instruments.requests.Add(ctx, 1, metric.WithAttributeSet(attrs))
	}

	f()
}

// RecordEventsIngestedBytes records the number of event bytes ingested.
func RecordEventsIngestedBytes(ctx context.Context, instruments *Instruments, em *EnvironmentManager, platformCategory string, ri RequestInfo, bytes int64) {
	if em == nil || instruments == nil || bytes <= 0 {
		return
	}
	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildRequestAttributes(em.envKVs, platformCategory, ua, wrapper, route, method, appID, appVersion, instanceID)
	instruments.eventsIngestedBytes.Add(ctx, bytes, metric.WithAttributeSet(attrs))
}

// RecordRequestDuration records a request duration measurement with the given attributes.
func RecordRequestDuration(ctx context.Context, instruments *Instruments, em *EnvironmentManager, ri RequestInfo, duration time.Duration, measure Measure) {
	if em == nil || instruments == nil || !measure.recordRequests {
		return
	}
	ua, wrapper, route, method, appID, appVersion, instanceID := ri.sanitized()
	attrs := buildRequestAttributes(em.envKVs, measure.platformCategory, ua, wrapper, route, method, appID, appVersion, instanceID)
	instruments.requestDuration.Record(ctx, duration.Seconds(), metric.WithAttributeSet(attrs))
}
