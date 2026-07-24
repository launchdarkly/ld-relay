package otelbridge

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// wiringStore is a minimal EnvStoreQueries whose initialized-but-empty state makes the server-side
// repository replay a single "put" event on connect.
type wiringStore struct{}

func (wiringStore) IsInitialized() bool { return true }

func (wiringStore) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{}, subsystems.NoSelector(), nil
}

// TestStreamProviderWiringRecordsMetrics exercises the whole path: a real StreamProvider built with
// the bridge's TraceFor, a real eventsource Server, an HTTP subscription, and a manual reader that
// captures what the ServerTrace callbacks record.
//
// With batch replay, the initial "put" a fresh subscriber receives is drained as a replay batch and
// flushed once, so it surfaces through the replay.* instruments and an eventsource.replay span rather
// than through events.sent / eventsource.write. A subsequent live "patch" published on the open stream
// is flushed individually and exercises the EventSent path end to end.
func TestStreamProviderWiringRecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instruments, err := metrics.NewInstrumentsForTest(meterProvider.Meter("test"))
	require.NoError(t, err)

	bridge := New(instruments.StreamInstruments(), []attribute.KeyValue{relayIDKey.String(testRelayID)})

	// Record spans into memory and point the bridge at that tracer, standing in for relay's global
	// tracer when OTLP is enabled.
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("test")
	bridge.tracer = tracer

	sp := streams.NewStreamProvider(basictypes.ServerSideStream, 0, 0, bridge.TraceFor)
	require.NotNil(t, sp)
	defer sp.Close()

	cred := sdkauth.New(config.SDKKey("sdk-key-wiring"))
	esp := sp.RegisterV1(cred, wiringStore{}, slog.Default())
	require.NotNil(t, esp)
	defer esp.Close()

	// Relay would register this channel where it registers the credential with the SSE servers.
	bridge.RegisterChannel(cred.String(), attribute.NewSet(
		relayIDKey.String(testRelayID), envNameKey.String(testEnvName)))

	handler := sp.HandlerV1(cred)
	require.NotNil(t, handler)

	// Relay's otelmux middleware holds a request span open for the connection; stand in for it so the
	// handler goroutine sees a recording span on its request context and the bridge emits child spans.
	tracedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "request")
		defer span.End()
		handler.ServeHTTP(w, r.WithContext(ctx))
	})

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, tracedHandler, func(eventCh <-chan eventsource.Event) {
		e := helpers.RequireValue(t, eventCh, time.Second, "timed out waiting for replayed event")
		require.Equal(t, "put", e.Event())

		// SubscriberAdded fires before the first event is written, so active is already settled.
		active := sumPoint(t, metricByName(t, collectRM(t, reader), "launchdarkly.relay.stream.subscribers.active"))
		assert.Equal(t, int64(1), active.Value)
		assertEnvAttrs(t, active.Attributes)
		assert.Equal(t, "server", attrValue(t, active.Attributes, streamKindAttrKey))
		assert.Equal(t, "v1", attrValue(t, active.Attributes, streamProtocolAttrKey))

		// The initial "put" is drained as a replay batch, so it is reported through ReplayFinished
		// rather than EventSent. The batch flush that ends the batch is what the client observed, so
		// poll for the replay.events metric to appear.
		require.Eventually(t, func() bool {
			return findMetricByName(collectRM(t, reader), "launchdarkly.relay.stream.replay.events") != nil
		}, time.Second, 5*time.Millisecond, "replay.events was never recorded")

		rm := collectRM(t, reader)
		replayEvents := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.events"))
		assert.GreaterOrEqual(t, replayEvents.Sum, int64(1))
		assertEnvAttrs(t, replayEvents.Attributes)

		replayBytes := histIntPoint(t, metricByName(t, rm, "launchdarkly.relay.stream.replay.bytes"))
		assert.Greater(t, replayBytes.Sum, int64(0), "the replayed put carries a non-empty data payload")
		assertEnvAttrs(t, replayBytes.Attributes)

		// The replay drain produces an eventsource.replay child span carrying the payload size.
		require.Eventually(t, func() bool {
			return replaySpanCount(spanRecorder) >= 1
		}, time.Second, 5*time.Millisecond, "no eventsource.replay span was recorded")
		replaySpan := endedSpanByName(t, spanRecorder.Ended(), spanReplay)
		assert.Greater(t, spanAttr(t, replaySpan, payloadSizeAttrKey).AsInt64(), int64(0))

		// Publish a live update on the open stream. It is flushed individually, so it exercises the
		// EventSent path: a "patch" event, an events.sent metric, and an eventsource.write span.
		esp.Apply(*makeLivePatch(t))

		patch := helpers.RequireValue(t, eventCh, time.Second, "timed out waiting for live patch event")
		require.Equal(t, "patch", patch.Event())

		require.Eventually(t, func() bool {
			return findMetricByName(collectRM(t, reader), "launchdarkly.relay.stream.events.sent") != nil
		}, time.Second, 5*time.Millisecond, "events.sent was never recorded for the live publish")

		sent := sumPoint(t, metricByName(t, collectRM(t, reader), "launchdarkly.relay.stream.events.sent"))
		assert.GreaterOrEqual(t, sent.Value, int64(1))
		assert.Equal(t, "patch", attrValue(t, sent.Attributes, eventTypeAttrKey))
		assertEnvAttrs(t, sent.Attributes)

		require.Eventually(t, func() bool {
			return writeSpanCount(spanRecorder) >= 1
		}, time.Second, 5*time.Millisecond, "no eventsource.write span was recorded for the live publish")
	})
}

// makeLivePatch builds an FDv1 delta change set that publishes a single flag put, which the
// server-side provider turns into a live "patch" event on the open stream.
func makeLivePatch(t *testing.T) *subsystems.ChangeSet {
	t.Helper()
	flag := ldbuilders.NewFlagBuilder("wiring-flag").Version(1).On(true).Build()
	flagJSON, err := ldmodel.NewJSONDataModelSerialization().MarshalFeatureFlag(flag)
	require.NoError(t, err)
	changeSet, err := subsystems.NewChangeSetBuilder().Start(subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "state",
			Target: 1,
			Code:   subsystems.IntentTransferChanges,
			Reason: "wiring-live-update",
		},
	}).AddPut(subsystems.FlagKind, flag.Key, 1, flagJSON).
		Finish(subsystems.NewSelector("state", 1))
	require.NoError(t, err)
	return changeSet
}

func writeSpanCount(recorder *tracetest.SpanRecorder) int {
	count := 0
	for _, s := range recorder.Ended() {
		if s.Name() == spanWrite {
			count++
		}
	}
	return count
}

func replaySpanCount(recorder *tracetest.SpanRecorder) int {
	count := 0
	for _, s := range recorder.Ended() {
		if s.Name() == spanReplay {
			count++
		}
	}
	return count
}

func collectRM(t *testing.T, reader sdkmetric.Reader) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))
	return &rm
}

func findMetricByName(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}
