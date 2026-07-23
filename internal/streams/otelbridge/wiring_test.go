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
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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
func TestStreamProviderWiringRecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instruments, err := metrics.NewInstrumentsForTest(meterProvider.Meter("test"))
	require.NoError(t, err)

	bridge := New(instruments.StreamInstruments(), []attribute.KeyValue{relayIDKey.String(testRelayID)})

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

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		e := helpers.RequireValue(t, eventCh, time.Second, "timed out waiting for replayed event")
		require.Equal(t, "put", e.Event())

		// SubscriberAdded fires before the first event is written, so active is already settled.
		active := sumPoint(t, metricByName(t, collectRM(t, reader), "launchdarkly.relay.stream.subscribers.active"))
		assert.Equal(t, int64(1), active.Value)
		assertEnvAttrs(t, active.Attributes)
		assert.Equal(t, "server", attrValue(t, active.Attributes, streamKindAttrKey))
		assert.Equal(t, "v1", attrValue(t, active.Attributes, streamProtocolAttrKey))

		// EventSent is recorded just after the flush the client observed, so poll for it.
		require.Eventually(t, func() bool {
			m := findMetricByName(collectRM(t, reader), "launchdarkly.relay.stream.events.sent")
			return m != nil
		}, time.Second, 5*time.Millisecond, "events.sent was never recorded")

		sent := sumPoint(t, metricByName(t, collectRM(t, reader), "launchdarkly.relay.stream.events.sent"))
		assert.GreaterOrEqual(t, sent.Value, int64(1))
		assert.Equal(t, "put", attrValue(t, sent.Attributes, eventTypeAttrKey))
		assertEnvAttrs(t, sent.Attributes)
	})
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
