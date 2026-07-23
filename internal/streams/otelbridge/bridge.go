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
package otelbridge

import (
	"context"
	"sync"

	"github.com/launchdarkly/ld-relay/v9/internal/metrics"

	"github.com/launchdarkly/eventsource"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// otherEventType is the bucket for any event type that is not on the relay allowlist, so a
	// future stream kind cannot silently explode event.type cardinality.
	otherEventType = "_other"
)

var (
	streamKindAttrKey     = attribute.Key("stream.kind")     //nolint:gochecknoglobals
	streamProtocolAttrKey = attribute.Key("stream.protocol") //nolint:gochecknoglobals
	endReasonAttrKey      = attribute.Key("end.reason")      //nolint:gochecknoglobals
	eventTypeAttrKey      = attribute.Key("event.type")      //nolint:gochecknoglobals
	discardReasonAttrKey  = attribute.Key("reason")          //nolint:gochecknoglobals
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

// Bridge holds the stream instruments and the channel-to-attributes registry shared by every
// eventsource Server's ServerTrace. It is safe for concurrent use.
type Bridge struct {
	instruments metrics.StreamInstruments
	baseAttrs   []attribute.KeyValue
	mu          sync.RWMutex
	channels    map[string][]attribute.KeyValue
}

// New creates a Bridge that records to the given stream instruments. baseAttrs is the process-level
// attribute set (relay.id) used as the fallback for channels that are not registered.
func New(instruments metrics.StreamInstruments, baseAttrs []attribute.KeyValue) *Bridge {
	return &Bridge{
		instruments: instruments,
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

// attrs builds the measurement attributes for a channel: the channel's environment attributes (or
// the fallback base attributes when the channel is unknown), plus this Server's stream.kind and
// stream.protocol, plus any callback-specific attributes.
func (t *streamTrace) attrs(channel string, extra ...attribute.KeyValue) metric.MeasurementOption {
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
	return metric.WithAttributeSet(attribute.NewSet(kvs...))
}

func (t *streamTrace) subscriberAdded(info eventsource.SubscriberAddedInfo) {
	t.bridge.instruments.SubscribersActive.Add(context.Background(), 1, t.attrs(info.Channel))
}

func (t *streamTrace) subscriberRemoved(info eventsource.SubscriberRemovedInfo) {
	t.bridge.instruments.SubscribersActive.Add(context.Background(), -1, t.attrs(info.Channel))
	t.bridge.instruments.ConnectionDuration.Record(context.Background(), info.ConnDuration.Seconds(),
		t.attrs(info.Channel, endReasonAttrKey.String(string(info.Reason))))
}

func (t *streamTrace) subscriberDropped(info eventsource.SubscriberDroppedInfo) {
	t.bridge.instruments.SubscribersDropped.Add(context.Background(), 1, t.attrs(info.Channel))
}

func (t *streamTrace) eventSent(info eventsource.EventSentInfo) {
	t.bridge.instruments.EventsSent.Add(context.Background(), 1,
		t.attrs(info.Channel, eventTypeAttrKey.String(eventTypeValue(info.EventType))))
	t.bridge.instruments.EventsSentSize.Record(context.Background(), int64(info.DataSize), t.attrs(info.Channel))
}

func (t *streamTrace) commentSent(info eventsource.CommentSentInfo) {
	t.bridge.instruments.CommentsSent.Add(context.Background(), 1, t.attrs(info.Channel))
}

func (t *streamTrace) eventDiscarded(info eventsource.EventDiscardedInfo) {
	t.bridge.instruments.EventsDiscarded.Add(context.Background(), 1,
		t.attrs(info.Channel, discardReasonAttrKey.String(string(info.Reason))))
}

func (t *streamTrace) writeError(info eventsource.WriteErrorInfo) {
	// The error value is deliberately not an attribute; it is unbounded and would explode cardinality.
	t.bridge.instruments.WriteErrors.Add(context.Background(), 1, t.attrs(info.Channel))
}

func (t *streamTrace) replayFinished(info eventsource.ReplayFinishedInfo) {
	t.bridge.instruments.ReplayEvents.Record(context.Background(), int64(info.EventCount), t.attrs(info.Channel))
	t.bridge.instruments.ReplayDrainDuration.Record(context.Background(), info.DrainDuration.Seconds(), t.attrs(info.Channel))
}

// eventTypeValue maps an event type to itself if it is on the relay allowlist, otherwise to the
// otherEventType bucket. An empty type is also bucketed.
func eventTypeValue(eventType string) string {
	if _, ok := knownEventTypes[eventType]; ok {
		return eventType
	}
	return otherEventType
}
