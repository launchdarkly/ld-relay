package otelbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// spanHarness bundles a bridge whose tracer records into an in-memory recorder, so tests can assert
// the spans and span events the callbacks emit.
type spanHarness struct {
	bridge   *Bridge
	recorder *tracetest.SpanRecorder
	tracer   trace.Tracer
}

func newSpanHarness(t *testing.T) *spanHarness {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	h := newBridgeHarness(t)
	h.bridge.tracer = tracer
	h.bridge.RegisterChannel(testChannel, envAttrs())
	return &spanHarness{bridge: h.bridge, recorder: recorder, tracer: tracer}
}

func (h *spanHarness) trace() *eventsource.ServerTrace {
	return h.bridge.TraceFor(testStreamKd, testProtocol)
}

// withRequestSpan starts a recording parent span, runs fn with its context, then ends it. It returns
// the parent's read-only span so tests can inspect its span events.
func (h *spanHarness) withRequestSpan(fn func(ctx context.Context)) sdktrace.ReadOnlySpan {
	ctx, parent := h.tracer.Start(context.Background(), "request")
	fn(ctx)
	parent.End()
	for _, s := range h.recorder.Ended() {
		if s.Name() == "request" {
			return s
		}
	}
	return nil
}

func endedSpanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	require.Failf(t, "span not found", "no span named %q", name)
	return nil
}

func spanAttr(t *testing.T, s sdktrace.ReadOnlySpan, key attribute.Key) attribute.Value {
	t.Helper()
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value
		}
	}
	require.Failf(t, "span attribute not found", "no attribute %q on span %q", key, s.Name())
	return attribute.Value{}
}

func eventByName(t *testing.T, s sdktrace.ReadOnlySpan, name string) sdktrace.Event {
	t.Helper()
	for _, e := range s.Events() {
		if e.Name == name {
			return e
		}
	}
	require.Failf(t, "span event not found", "no event %q on span %q", name, s.Name())
	return sdktrace.Event{}
}

func eventAttr(t *testing.T, e sdktrace.Event, key attribute.Key) attribute.Value {
	t.Helper()
	for _, kv := range e.Attributes {
		if kv.Key == key {
			return kv.Value
		}
	}
	require.Failf(t, "event attribute not found", "no attribute %q on event %q", key, e.Name)
	return attribute.Value{}
}

func TestEventSentEmitsBackDatedWriteSpan(t *testing.T) {
	h := newSpanHarness(t)
	const writeDuration = 40 * time.Millisecond

	parent := h.withRequestSpan(func(ctx context.Context) {
		h.trace().EventSent(ctx, eventsource.EventSentInfo{
			Channel:       testChannel,
			EventType:     "put",
			DataSize:      256,
			WriteDuration: writeDuration,
		})
	})

	span := endedSpanByName(t, h.recorder.Ended(), spanWrite)
	assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID(), "write span must be a child of the request span")
	assert.Equal(t, writeDuration, span.EndTime().Sub(span.StartTime()), "write span is back-dated by WriteDuration")

	assert.Equal(t, writeTypeEvent, spanAttr(t, span, writeTypeAttrKey).AsString())
	assert.Equal(t, "put", spanAttr(t, span, eventTypeAttrKey).AsString())
	assert.Equal(t, int64(256), spanAttr(t, span, payloadSizeAttrKey).AsInt64())
	assert.Equal(t, testStreamKd, spanAttr(t, span, streamKindAttrKey).AsString())
	assert.Equal(t, testProtocol, spanAttr(t, span, streamProtocolAttrKey).AsString())
	assert.Equal(t, testEnvName, spanAttr(t, span, envNameKey).AsString())
}

func TestEventSentWriteSpanBucketsUnknownEventType(t *testing.T) {
	h := newSpanHarness(t)

	h.withRequestSpan(func(ctx context.Context) {
		h.trace().EventSent(ctx, eventsource.EventSentInfo{
			Channel:   testChannel,
			EventType: "some-future-type",
			DataSize:  1,
		})
	})

	span := endedSpanByName(t, h.recorder.Ended(), spanWrite)
	assert.Equal(t, otherEventType, spanAttr(t, span, eventTypeAttrKey).AsString())
}

func TestCommentSentEmitsWriteSpanWithoutEventAttrs(t *testing.T) {
	h := newSpanHarness(t)
	const writeDuration = 15 * time.Millisecond

	h.withRequestSpan(func(ctx context.Context) {
		h.trace().CommentSent(ctx, eventsource.CommentSentInfo{Channel: testChannel, WriteDuration: writeDuration})
	})

	span := endedSpanByName(t, h.recorder.Ended(), spanWrite)
	assert.Equal(t, writeTypeComment, spanAttr(t, span, writeTypeAttrKey).AsString())
	assert.Equal(t, writeDuration, span.EndTime().Sub(span.StartTime()))

	// A comment carries neither an event type nor a payload size.
	for _, kv := range span.Attributes() {
		assert.NotEqual(t, eventTypeAttrKey, kv.Key)
		assert.NotEqual(t, payloadSizeAttrKey, kv.Key)
	}
}

func TestReplayFinishedEmitsBackDatedReplaySpan(t *testing.T) {
	h := newSpanHarness(t)
	const drainDuration = 120 * time.Millisecond

	parent := h.withRequestSpan(func(ctx context.Context) {
		h.trace().ReplayFinished(ctx, eventsource.ReplayFinishedInfo{
			Channel:       testChannel,
			EventCount:    7,
			TotalDataSize: 4096,
			DrainDuration: drainDuration,
		})
	})

	span := endedSpanByName(t, h.recorder.Ended(), spanReplay)
	assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID(), "replay span must be a child of the request span")
	assert.Equal(t, drainDuration, span.EndTime().Sub(span.StartTime()), "replay span is back-dated by DrainDuration")
	assert.Equal(t, int64(7), spanAttr(t, span, eventCountAttrKey).AsInt64())
	assert.Equal(t, int64(4096), spanAttr(t, span, payloadSizeAttrKey).AsInt64())
	assert.Equal(t, false, spanAttr(t, span, replayAbortedAttrKey).AsBool())
	assert.Equal(t, testStreamKd, spanAttr(t, span, streamKindAttrKey).AsString())
	assert.Equal(t, testProtocol, spanAttr(t, span, streamProtocolAttrKey).AsString())
	assert.Equal(t, testEnvName, spanAttr(t, span, envNameKey).AsString())
}

func TestSubscribeUnsubscribeEmitSpanEventsOnRequestSpan(t *testing.T) {
	h := newSpanHarness(t)

	parent := h.withRequestSpan(func(ctx context.Context) {
		tr := h.trace()
		tr.SubscriberAdded(ctx, eventsource.SubscriberAddedInfo{Channel: testChannel})
		tr.SubscriberRemoved(ctx, eventsource.SubscriberRemovedInfo{
			Channel:      testChannel,
			Reason:       eventsource.ReasonClientClosed,
			ConnDuration: 2500 * time.Millisecond,
		})
	})

	// The subscribe/unsubscribe points are events on the request span, not new spans.
	for _, s := range h.recorder.Ended() {
		assert.NotEqual(t, spanWrite, s.Name())
		assert.NotEqual(t, spanReplay, s.Name())
	}

	require.NotNil(t, parent)
	eventByName(t, parent, eventSubscribed)

	unsub := eventByName(t, parent, eventUnsubscribed)
	assert.Equal(t, "client_closed", eventAttr(t, unsub, endReasonAttrKey).AsString())
	assert.Equal(t, int64(2500), eventAttr(t, unsub, connDurationAttrKey).AsInt64())
}

func TestWriteErrorEmitsSpanEventWithMessage(t *testing.T) {
	h := newSpanHarness(t)

	parent := h.withRequestSpan(func(ctx context.Context) {
		h.trace().WriteError(ctx, eventsource.WriteErrorInfo{Channel: testChannel, Err: errors.New("broken pipe")})
	})

	require.NotNil(t, parent)
	ev := eventByName(t, parent, eventWriteError)
	assert.Equal(t, "broken pipe", eventAttr(t, ev, errorMessageAttrKey).AsString())
}

func TestNoRecordingSpanProducesNoSpans(t *testing.T) {
	h := newSpanHarness(t)
	tr := h.trace()

	// context.Background() carries no recording span, so nothing should be created and nothing panics.
	assert.NotPanics(t, func() {
		tr.SubscriberAdded(context.Background(), eventsource.SubscriberAddedInfo{Channel: testChannel})
		tr.EventSent(context.Background(), eventsource.EventSentInfo{Channel: testChannel, EventType: "put", DataSize: 8, WriteDuration: time.Millisecond})
		tr.CommentSent(context.Background(), eventsource.CommentSentInfo{Channel: testChannel, WriteDuration: time.Millisecond})
		tr.WriteError(context.Background(), eventsource.WriteErrorInfo{Channel: testChannel, Err: errors.New("x")})
		tr.SubscriberRemoved(context.Background(), eventsource.SubscriberRemovedInfo{Channel: testChannel, Reason: eventsource.ReasonClientClosed})
		tr.ReplayFinished(context.Background(), eventsource.ReplayFinishedInfo{Channel: testChannel, EventCount: 1, DrainDuration: time.Millisecond})
	})

	assert.Empty(t, h.recorder.Ended(), "no spans should be created without a recording request span")
}
