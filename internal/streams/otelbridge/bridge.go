// Package otelbridge implements the eventsource ServerTrace callbacks in terms of the OTel stream
// instruments owned by the metrics package. It is the ld-relay-side implementation of the
// vendor-neutral hooks that the eventsource library exposes for its SSE Server.
//
// The bridge translates a callback's channel string (an sdkauth.ScopedCredential string form) into
// the environment attribute set that relay uses for all of its metrics. Because the callbacks run on
// eventsource-internal goroutines -- including the Server's single dispatch goroutine -- the lookup
// is a lock-light read of a map that relay populates when it registers credentials with the SSE
// servers. A channel that is not in the registry still records, using a minimal fallback attribute
// set, so a measurement is never dropped.
//
// The handler-goroutine callbacks also receive the subscriber's request context. When that context
// carries a recording span (relay's otelmux middleware holds one open for the connection's lifetime),
// the bridge emits short child spans for event/comment writes and for replay drains, back-dated from
// the durations the library measured, plus span events for subscribe/unsubscribe/write-error. When no
// recording span is present -- including when OTLP is disabled and the global tracer is a noop -- no
// spans are created, so the trace path costs one branch.
package otelbridge

import (
	"context"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/eventsource"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	// otherEventType is the bucket for any event type that is not on the relay allowlist, so a
	// future stream kind cannot silently explode event.type cardinality.
	otherEventType = "_other"

	// writeTypeEvent and writeTypeComment label an eventsource.write span by what was written.
	writeTypeEvent   = "event"
	writeTypeComment = "comment"

	// Span names for the short child spans the bridge nests under the request span.
	spanWrite  = "eventsource.write"
	spanReplay = "eventsource.replay"

	// Span event names added to the request span at points in time.
	eventSubscribed   = "eventsource.subscribed"
	eventUnsubscribed = "eventsource.unsubscribed"
	eventWriteError   = "eventsource.write_error"
)

var (
	streamKindAttrKey     = attribute.Key("stream.kind")            //nolint:gochecknoglobals
	streamProtocolAttrKey = attribute.Key("stream.protocol")        //nolint:gochecknoglobals
	endReasonAttrKey      = attribute.Key("end.reason")             //nolint:gochecknoglobals
	eventTypeAttrKey      = attribute.Key("event.type")             //nolint:gochecknoglobals
	discardReasonAttrKey  = attribute.Key("reason")                 //nolint:gochecknoglobals
	writeTypeAttrKey      = attribute.Key("write.type")             //nolint:gochecknoglobals
	payloadSizeAttrKey    = attribute.Key("payload.size")           //nolint:gochecknoglobals
	eventCountAttrKey     = attribute.Key("event.count")            //nolint:gochecknoglobals
	connDurationAttrKey   = attribute.Key("connection.duration_ms") //nolint:gochecknoglobals
	errorMessageAttrKey   = attribute.Key("error.message")          //nolint:gochecknoglobals
)

// knownEventTypes is the set of SSE event types that relay publishes. Anything else is bucketed as
// otherEventType. The FDv1 streams publish put/patch/delete/ping; the FDv2 streams publish the
// data-system protocol events (server-intent/put-object/delete-object/payload-transferred).
var knownEventTypes = map[string]struct{}{ //nolint:gochecknoglobals
	"put":                 {},
	"patch":               {},
	"delete":              {},
	"ping":                {},
	"server-intent":       {},
	"put-object":          {},
	"delete-object":       {},
	"payload-transferred": {},
}

// Bridge holds the stream instruments, the tracer, and the channel-to-attributes registry shared by
// every eventsource Server's ServerTrace. It is safe for concurrent use.
type Bridge struct {
	instruments metrics.StreamInstruments
	tracer      trace.Tracer
	baseAttrs   []attribute.KeyValue
	mu          sync.RWMutex
	channels    map[string][]attribute.KeyValue
}

// New creates a Bridge that records to the given stream instruments. baseAttrs is the process-level
// attribute set (relay.id) used as the fallback for channels that are not registered. The tracer is
// relay's shared tracer, which is a noop when OTLP is disabled.
func New(instruments metrics.StreamInstruments, baseAttrs []attribute.KeyValue) *Bridge {
	return &Bridge{
		instruments: instruments,
		tracer:      tracing.Tracer(),
		baseAttrs:   baseAttrs,
		channels:    make(map[string][]attribute.KeyValue),
	}
}

// RegisterChannel associates a channel string with the environment attribute set that its metrics
// should carry. It is called when a credential is registered with the SSE servers.
func (b *Bridge) RegisterChannel(channel string, attrs attribute.Set) {
	kvs := attrs.ToSlice()
	b.mu.Lock()
	b.channels[channel] = kvs
	b.mu.Unlock()
}

// UnregisterChannel removes a channel from the registry. It is called when a credential is removed
// from the SSE servers (key rotation, credential expiry, or environment teardown).
func (b *Bridge) UnregisterChannel(channel string) {
	b.mu.Lock()
	delete(b.channels, channel)
	b.mu.Unlock()
}

// TraceFor returns a ServerTrace whose callbacks record the stream instruments with the given
// stream.kind and stream.protocol baked in, alongside the per-channel environment attributes. The
// returned value is meant to be attached to a single eventsource.Server.
func (b *Bridge) TraceFor(streamKind, protocol string) *eventsource.ServerTrace {
	t := &streamTrace{
		bridge:     b,
		kindKV:     streamKindAttrKey.String(streamKind),
		protocolKV: streamProtocolAttrKey.String(protocol),
	}
	return &eventsource.ServerTrace{
		SubscriberAdded:   t.subscriberAdded,
		SubscriberRemoved: t.subscriberRemoved,
		SubscriberDropped: t.subscriberDropped,
		EventSent:         t.eventSent,
		CommentSent:       t.commentSent,
		EventDiscarded:    t.eventDiscarded,
		WriteError:        t.writeError,
		ReplayFinished:    t.replayFinished,
	}
}

// streamTrace holds the per-Server tracing state: the bridge and the stream.kind/stream.protocol
// attributes for this Server. Its methods are the ServerTrace callbacks.
type streamTrace struct {
	bridge     *Bridge
	kindKV     attribute.KeyValue
	protocolKV attribute.KeyValue
}

// channelAttrs builds the attributes for a channel: the channel's environment attributes (or the
// fallback base attributes when the channel is unknown), plus this Server's stream.kind and
// stream.protocol, plus any call-specific attributes. The slice is used for both metric and span
// attribution.
func (t *streamTrace) channelAttrs(channel string, extra ...attribute.KeyValue) []attribute.KeyValue {
	t.bridge.mu.RLock()
	base, ok := t.bridge.channels[channel]
	t.bridge.mu.RUnlock()
	if !ok {
		base = t.bridge.baseAttrs
	}
	kvs := make([]attribute.KeyValue, 0, len(base)+2+len(extra))
	kvs = append(kvs, base...)
	kvs = append(kvs, t.kindKV, t.protocolKV)
	kvs = append(kvs, extra...)
	return kvs
}

// attrs wraps channelAttrs as a metric measurement option.
func (t *streamTrace) attrs(channel string, extra ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(t.channelAttrs(channel, extra...)...))
}

// writeSpan emits a short eventsource.write span nested under the request span, back-dated so its
// start is duration before now. It creates nothing when the request context has no recording span,
// which is also the noop-tracer (OTLP-disabled) case.
func (t *streamTrace) writeSpan(ctx context.Context, channel, writeType string, duration time.Duration, extra ...attribute.KeyValue) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return
	}
	end := time.Now()
	attrs := t.channelAttrs(channel, append([]attribute.KeyValue{writeTypeAttrKey.String(writeType)}, extra...)...)
	_, span := t.bridge.tracer.Start(ctx, spanWrite,
		trace.WithTimestamp(end.Add(-duration)),
		trace.WithAttributes(attrs...))
	span.End(trace.WithTimestamp(end))
}

// replaySpan emits a short eventsource.replay span nested under the request span, back-dated by the
// drain duration the handler observed. Same recording gate as writeSpan.
func (t *streamTrace) replaySpan(ctx context.Context, channel string, count int, duration time.Duration) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return
	}
	end := time.Now()
	attrs := t.channelAttrs(channel, eventCountAttrKey.Int(count))
	_, span := t.bridge.tracer.Start(ctx, spanReplay,
		trace.WithTimestamp(end.Add(-duration)),
		trace.WithAttributes(attrs...))
	span.End(trace.WithTimestamp(end))
}

// addSpanEvent records a point-in-time event on the request span, if it is recording.
func (t *streamTrace) addSpanEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

func (t *streamTrace) subscriberAdded(ctx context.Context, info eventsource.SubscriberAddedInfo) {
	t.bridge.instruments.SubscribersActive.Add(context.Background(), 1, t.attrs(info.Channel))
	t.addSpanEvent(ctx, eventSubscribed)
}

func (t *streamTrace) subscriberRemoved(ctx context.Context, info eventsource.SubscriberRemovedInfo) {
	t.bridge.instruments.SubscribersActive.Add(context.Background(), -1, t.attrs(info.Channel))
	t.bridge.instruments.ConnectionDuration.Record(context.Background(), info.ConnDuration.Seconds(),
		t.attrs(info.Channel, endReasonAttrKey.String(string(info.Reason))))
	t.addSpanEvent(ctx, eventUnsubscribed,
		endReasonAttrKey.String(string(info.Reason)),
		connDurationAttrKey.Int64(info.ConnDuration.Milliseconds()))
}

func (t *streamTrace) subscriberDropped(info eventsource.SubscriberDroppedInfo) {
	// No context: this fires on the Server's dispatch goroutine, not a subscriber's handler.
	t.bridge.instruments.SubscribersDropped.Add(context.Background(), 1, t.attrs(info.Channel))
}

func (t *streamTrace) eventSent(ctx context.Context, info eventsource.EventSentInfo) {
	eventType := eventTypeValue(info.EventType)
	t.bridge.instruments.EventsSent.Add(context.Background(), 1,
		t.attrs(info.Channel, eventTypeAttrKey.String(eventType)))
	t.bridge.instruments.EventsSentSize.Record(context.Background(), int64(info.DataSize), t.attrs(info.Channel))
	t.writeSpan(ctx, info.Channel, writeTypeEvent, info.WriteDuration,
		eventTypeAttrKey.String(eventType),
		payloadSizeAttrKey.Int(info.DataSize))
}

func (t *streamTrace) commentSent(ctx context.Context, info eventsource.CommentSentInfo) {
	t.bridge.instruments.CommentsSent.Add(context.Background(), 1, t.attrs(info.Channel))
	t.writeSpan(ctx, info.Channel, writeTypeComment, info.WriteDuration)
}

func (t *streamTrace) eventDiscarded(_ context.Context, info eventsource.EventDiscardedInfo) {
	t.bridge.instruments.EventsDiscarded.Add(context.Background(), 1,
		t.attrs(info.Channel, discardReasonAttrKey.String(string(info.Reason))))
}

func (t *streamTrace) writeError(ctx context.Context, info eventsource.WriteErrorInfo) {
	// The error value is deliberately not a metric attribute; it is unbounded and would explode
	// cardinality. Span events do not share that constraint, so the message is attached there.
	t.bridge.instruments.WriteErrors.Add(context.Background(), 1, t.attrs(info.Channel))
	var attrs []attribute.KeyValue
	if info.Err != nil {
		attrs = append(attrs, errorMessageAttrKey.String(info.Err.Error()))
	}
	t.addSpanEvent(ctx, eventWriteError, attrs...)
}

func (t *streamTrace) replayFinished(ctx context.Context, info eventsource.ReplayFinishedInfo) {
	t.bridge.instruments.ReplayEvents.Record(context.Background(), int64(info.EventCount), t.attrs(info.Channel))
	t.bridge.instruments.ReplayDrainDuration.Record(context.Background(), info.DrainDuration.Seconds(), t.attrs(info.Channel))
	t.replaySpan(ctx, info.Channel, info.EventCount, info.DrainDuration)
}

// eventTypeValue maps an event type to itself if it is on the relay allowlist, otherwise to the
// otherEventType bucket. An empty type is also bucketed.
func eventTypeValue(eventType string) string {
	if _, ok := knownEventTypes[eventType]; ok {
		return eventType
	}
	return otherEventType
}
