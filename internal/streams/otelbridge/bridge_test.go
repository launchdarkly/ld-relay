package otelbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/metrics"

	"github.com/launchdarkly/eventsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testChannel  = "sdk-key-123"
	testRelayID  = "relay-abc"
	testEnvName  = "my-env"
	testStreamKd = "server"
	testProtocol = "v1"
)

var (
	relayIDKey = attribute.Key("relay.id")
	envNameKey = attribute.Key("environment.name")
)

// bridgeHarness bundles a bridge with the manual reader that captures what it records.
type bridgeHarness struct {
	bridge *Bridge
	reader sdkmetric.Reader
}

func newBridgeHarness(t *testing.T) *bridgeHarness {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instruments, err := metrics.NewInstrumentsForTest(meterProvider.Meter("test"))
	require.NoError(t, err)

	baseAttrs := []attribute.KeyValue{relayIDKey.String(testRelayID)}
	return &bridgeHarness{
		bridge: New(instruments.StreamInstruments(), baseAttrs),
		reader: reader,
	}
}

// envAttrs is the attribute set relay would register for an environment's channel.
func envAttrs() attribute.Set {
	return attribute.NewSet(relayIDKey.String(testRelayID), envNameKey.String(testEnvName))
}

func (h *bridgeHarness) collect(t *testing.T) *metricdata.ResourceMetrics {
	var rm metricdata.ResourceMetrics
	require.NoError(t, h.reader.Collect(context.Background(), &rm))
	return &rm
}

func (h *bridgeHarness) trace() *eventsource.ServerTrace {
	return h.bridge.TraceFor(testStreamKd, testProtocol)
}

func metricByName(t *testing.T, rm *metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	require.Failf(t, "metric not found", "no metric named %q", name)
	return metricdata.Metrics{}
}

func requireNoMetric(t *testing.T, rm *metricdata.ResourceMetrics, name string) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			require.NotEqualf(t, name, m.Name, "expected no data for metric %q", name)
		}
	}
}

func sumPoint(t *testing.T, m metricdata.Metrics) metricdata.DataPoint[int64] {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.Truef(t, ok, "expected Sum[int64] for %s", m.Name)
	require.Lenf(t, sum.DataPoints, 1, "expected one data point for %s", m.Name)
	return sum.DataPoints[0]
}

func histFloatPoint(t *testing.T, m metricdata.Metrics) metricdata.HistogramDataPoint[float64] {
	t.Helper()
	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.Truef(t, ok, "expected Histogram[float64] for %s", m.Name)
	require.Lenf(t, hist.DataPoints, 1, "expected one data point for %s", m.Name)
	return hist.DataPoints[0]
}

func histIntPoint(t *testing.T, m metricdata.Metrics) metricdata.HistogramDataPoint[int64] {
	t.Helper()
	hist, ok := m.Data.(metricdata.Histogram[int64])
	require.Truef(t, ok, "expected Histogram[int64] for %s", m.Name)
	require.Lenf(t, hist.DataPoints, 1, "expected one data point for %s", m.Name)
	return hist.DataPoints[0]
}

func attrValue(t *testing.T, set attribute.Set, key attribute.Key) string {
	t.Helper()
	v, ok := set.Value(key)
	require.Truef(t, ok, "attribute %q not present", key)
	return v.AsString()
}

func boolAttrValue(t *testing.T, set attribute.Set, key attribute.Key) bool {
	t.Helper()
	v, ok := set.Value(key)
	require.Truef(t, ok, "attribute %q not present", key)
	return v.AsBool()
}

// assertStreamKindAndProtocol checks that every metric carries the baked-in stream identity.
func assertStreamKindAndProtocol(t *testing.T, set attribute.Set) {
	t.Helper()
	assert.Equal(t, testStreamKd, attrValue(t, set, streamKindAttrKey))
	assert.Equal(t, testProtocol, attrValue(t, set, streamProtocolAttrKey))
}

// assertEnvAttrs checks that a metric was attributed to the registered environment.
func assertEnvAttrs(t *testing.T, set attribute.Set) {
	t.Helper()
	assert.Equal(t, testRelayID, attrValue(t, set, relayIDKey))
	assert.Equal(t, testEnvName, attrValue(t, set, envNameKey))
}

func TestSubscriberAddedIncrementsActive(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().SubscriberAdded(context.Background(), eventsource.SubscriberAddedInfo{Channel: testChannel})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.subscribers.active"))
	assert.Equal(t, int64(1), dp.Value)
	assertEnvAttrs(t, dp.Attributes)
	assertStreamKindAndProtocol(t, dp.Attributes)
}

func TestSubscriberRemovedDecrementsActiveAndRecordsDuration(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())
	tr := h.trace()

	tr.SubscriberAdded(context.Background(), eventsource.SubscriberAddedInfo{Channel: testChannel})
	tr.SubscriberRemoved(context.Background(), eventsource.SubscriberRemovedInfo{
		Channel:      testChannel,
		Reason:       eventsource.ReasonClientClosed,
		ConnDuration: 3 * time.Second,
	})

	rm := h.collect(t)

	active := sumPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.subscribers.active"))
	assert.Equal(t, int64(0), active.Value)

	dur := histFloatPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.connection.duration"))
	assert.Equal(t, uint64(1), dur.Count)
	assert.InDelta(t, 3.0, dur.Sum, 0.001)
	assert.Equal(t, "client_closed", attrValue(t, dur.Attributes, endReasonAttrKey))
	assertEnvAttrs(t, dur.Attributes)
	assertStreamKindAndProtocol(t, dur.Attributes)
}

func TestSubscriberDropped(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().SubscriberDropped(eventsource.SubscriberDroppedInfo{Channel: testChannel, BufferSize: 32})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.subscribers.dropped"))
	assert.Equal(t, int64(1), dp.Value)
	assertEnvAttrs(t, dp.Attributes)
	assertStreamKindAndProtocol(t, dp.Attributes)
}

func TestEventSentRecordsCountWithTypeAndSize(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().EventSent(context.Background(), eventsource.EventSentInfo{Channel: testChannel, EventType: "put", DataSize: 128})

	rm := h.collect(t)

	sent := sumPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.events.sent"))
	assert.Equal(t, int64(1), sent.Value)
	assert.Equal(t, "put", attrValue(t, sent.Attributes, eventTypeAttrKey))
	assertEnvAttrs(t, sent.Attributes)
	assertStreamKindAndProtocol(t, sent.Attributes)

	size := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.events.sent.size"))
	assert.Equal(t, uint64(1), size.Count)
	assert.Equal(t, int64(128), size.Sum)
	// The size histogram is not broken down by event type.
	_, hasType := size.Attributes.Value(eventTypeAttrKey)
	assert.False(t, hasType)
}

func TestEventSentBucketsUnknownAndFDv2Types(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
	}{
		{"put", "put"},
		{"patch", "patch"},
		{"delete", "delete"},
		{"ping", "ping"},
		{"server-intent", "server-intent"},
		{"put-object", "put-object"},
		{"delete-object", "delete-object"},
		{"payload-transferred", "payload-transferred"},
		{"goodbye", "_other"},
		{"", "_other"},
		{"some-future-type", "_other"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			h := newBridgeHarness(t)
			h.bridge.RegisterChannel(testChannel, envAttrs())

			h.trace().EventSent(context.Background(), eventsource.EventSentInfo{Channel: testChannel, EventType: c.raw, DataSize: 1})

			dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.events.sent"))
			assert.Equal(t, c.expected, attrValue(t, dp.Attributes, eventTypeAttrKey))
		})
	}
}

func TestCommentSent(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().CommentSent(context.Background(), eventsource.CommentSentInfo{Channel: testChannel})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.comments.sent"))
	assert.Equal(t, int64(1), dp.Value)
	assertEnvAttrs(t, dp.Attributes)
	assertStreamKindAndProtocol(t, dp.Attributes)
}

func TestEventDiscarded(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().EventDiscarded(context.Background(), eventsource.EventDiscardedInfo{
		Channel: testChannel,
		Reason:  eventsource.DiscardReasonJitterCoalesce,
	})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.events.discarded"))
	assert.Equal(t, int64(1), dp.Value)
	assert.Equal(t, "jitter_coalesce", attrValue(t, dp.Attributes, discardReasonAttrKey))
	assertEnvAttrs(t, dp.Attributes)
}

func TestWriteError(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().WriteError(context.Background(), eventsource.WriteErrorInfo{Channel: testChannel, Err: errors.New("broken pipe")})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.write.errors"))
	assert.Equal(t, int64(1), dp.Value)
	assertEnvAttrs(t, dp.Attributes)
	assertStreamKindAndProtocol(t, dp.Attributes)
}

func TestReplayFinishedRecordsEventsSizeAndDuration(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().ReplayFinished(context.Background(), eventsource.ReplayFinishedInfo{
		Channel:       testChannel,
		EventCount:    5,
		TotalDataSize: 2048,
		DrainDuration: 250 * time.Millisecond,
	})

	rm := h.collect(t)

	events := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.events"))
	assert.Equal(t, uint64(1), events.Count)
	assert.Equal(t, int64(5), events.Sum)
	assertEnvAttrs(t, events.Attributes)
	assert.Equal(t, false, boolAttrValue(t, events.Attributes, replayAbortedAttrKey))

	// Replayed events do not fire EventSent, so replay.data.size is the only
	// byte-volume record for the batch.
	size := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.data.size"))
	assert.Equal(t, uint64(1), size.Count)
	assert.Equal(t, int64(2048), size.Sum)
	assertEnvAttrs(t, size.Attributes)

	drain := histFloatPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.drain.duration"))
	assert.Equal(t, uint64(1), drain.Count)
	assert.InDelta(t, 0.25, drain.Sum, 0.001)
}

func TestReplayFinishedAbortedIsAttributed(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.trace().ReplayFinished(context.Background(), eventsource.ReplayFinishedInfo{
		Channel:       testChannel,
		EventCount:    3,
		TotalDataSize: 300,
		DrainDuration: 50 * time.Millisecond,
		Aborted:       true,
	})

	rm := h.collect(t)

	events := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.events"))
	assert.Equal(t, int64(3), events.Sum)
	assert.Equal(t, true, boolAttrValue(t, events.Attributes, replayAbortedAttrKey),
		"a partially-drained batch must be distinguishable from a completed one")

	size := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.data.size"))
	assert.Equal(t, int64(300), size.Sum)
	assert.Equal(t, true, boolAttrValue(t, size.Attributes, replayAbortedAttrKey))
}

func TestUnknownChannelUsesFallbackAttributes(t *testing.T) {
	h := newBridgeHarness(t)
	// No RegisterChannel: the channel is unknown.

	h.trace().SubscriberAdded(context.Background(), eventsource.SubscriberAddedInfo{Channel: "never-registered"})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.subscribers.active"))
	assert.Equal(t, int64(1), dp.Value, "measurement must not be dropped for an unknown channel")
	assert.Equal(t, testRelayID, attrValue(t, dp.Attributes, relayIDKey))
	assertStreamKindAndProtocol(t, dp.Attributes)
	// The fallback set has no environment name.
	_, hasEnv := dp.Attributes.Value(envNameKey)
	assert.False(t, hasEnv)
}

func TestUnregisterChannelRevertsToFallback(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())
	h.bridge.UnregisterChannel(testChannel)

	h.trace().SubscriberAdded(context.Background(), eventsource.SubscriberAddedInfo{Channel: testChannel})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.subscribers.active"))
	_, hasEnv := dp.Attributes.Value(envNameKey)
	assert.False(t, hasEnv, "after unregister, the channel falls back to the base attributes")
}

func TestTraceForBakesInDistinctStreamIdentity(t *testing.T) {
	h := newBridgeHarness(t)
	h.bridge.RegisterChannel(testChannel, envAttrs())

	h.bridge.TraceFor("mobile-ping", "v2").SubscriberDropped(
		eventsource.SubscriberDroppedInfo{Channel: testChannel})

	dp := sumPoint(t, metricByName(t, h.collect(t), "launchdarkly.relay.stream.subscribers.dropped"))
	assert.Equal(t, "mobile-ping", attrValue(t, dp.Attributes, streamKindAttrKey))
	assert.Equal(t, "v2", attrValue(t, dp.Attributes, streamProtocolAttrKey))
}

func TestReplayStartedHookIsNotSet(t *testing.T) {
	// The bridge records nothing on ReplayStarted; only ReplayFinished carries metrics. Leaving the
	// hook nil keeps eventsource on its cheaper path for that call site.
	assert.Nil(t, newBridgeHarness(t).trace().ReplayStarted)
}

func TestEventTypeValue(t *testing.T) {
	assert.Equal(t, "put", eventTypeValue("put"))
	assert.Equal(t, "payload-transferred", eventTypeValue("payload-transferred"))
	assert.Equal(t, otherEventType, eventTypeValue("bogus"))
	assert.Equal(t, otherEventType, eventTypeValue(""))
}
