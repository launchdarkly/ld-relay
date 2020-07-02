package metrics

import (
	"context"
	"testing"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/internal/events"
)

const testReportingPeriod = time.Millisecond

func init() {
	view.SetReportingPeriod(testReportingPeriod)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
}

func TestOpenCensusEventsExporter(t *testing.T) {
	withTestView := func(publisher events.EventPublisher, f func(ctx context.Context, exporter *OpenCensusEventsExporter)) {
		relayId := uuid.New()
		exporter := newOpenCensusEventsExporter(relayId, publisher, time.Millisecond)
		view.RegisterExporter(exporter)
		defer func() {
			view.UnregisterExporter(exporter)
			// Wait for any views to drain
			time.Sleep(testReportingPeriod)
		}()
		ctx, err := tag.New(
			context.Background(),
			tag.Insert(relayIdTagKey, relayId),
			tag.Insert(platformCategoryTagKey, "gameConsole"),
			tag.Insert(userAgentTagKey, "my-agent"))
		require.NoError(t, err)
		metricView := &view.View{
			Measure:     privateConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIdTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		require.NoError(t, view.Register(metricView))
		defer view.Unregister(metricView)
		f(ctx, exporter)
	}

	t.Run("exporter generates events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		start := nowInUnixMillis()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			stats.Record(ctx, privateConnMeasure.M(1))
			var event interface{}
			select {
			case event = <-publisher.events:
				break
			case <-time.After(time.Second):
				require.Fail(t, "timed out")
			}
			require.IsType(t, RelayMetricsEvent{}, event)
			metricsEvent := event.(RelayMetricsEvent)
			require.Equal(t, RelayMetricsKind, metricsEvent.Kind)
			assert.True(t, metricsEvent.StartDate >= start/int64(time.Millisecond))
			assert.True(t, metricsEvent.StartDate <= metricsEvent.EndDate)
			assert.True(t, metricsEvent.EndDate <= nowInUnixMillis())
			expectedRelayId, _ := tag.FromContext(ctx).Value(relayIdTagKey)
			assert.Equal(t, expectedRelayId, metricsEvent.RelayId)
			if !assert.ElementsMatch(t, []currentConnectionsMetric{{
				UserAgent:        "my-agent",
				PlatformCategory: "gameConsole",
				Current:          1,
			}}, metricsEvent.Connections) {
				t.Logf("Received events were: %+v", metricsEvent.Connections)
			}
		})
	})

	t.Run("empty metrics generate no events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			stats.Record(ctx, privateConnMeasure.M(0))
			select {
			case event := <-publisher.events:
				require.Fail(t, "expected no events", "got one: %+v", event)
			case <-time.After(time.Millisecond * 10):
			}
		})
	})

	t.Run("the event start time still shifts when events are not sent", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			time.Sleep(time.Millisecond * 10)
			startTime := nowInUnixMillis()
			// Wait an extra moment to let any export operation that has already started complete
			time.Sleep(time.Millisecond * 1)
			stats.Record(ctx, privateConnMeasure.M(1))
			var event interface{}
			select {
			case event = <-publisher.events:
				break
			case <-time.After(time.Second):
				require.Fail(t, "timed out")
			}
			require.IsType(t, RelayMetricsEvent{}, event)
			metricsEvent := event.(RelayMetricsEvent)
			assert.True(t, metricsEvent.StartDate >= startTime)
		})
	})

	t.Run("it ignores metrics for other relays", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			ctxForDifferentRelay, _ := tag.New(ctx, tag.Upsert(relayIdTagKey, uuid.New()))
			stats.Record(ctxForDifferentRelay, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))
			timeout := time.After(time.Second)
			for {
				select {
				case event := <-publisher.events:
					metricsEvent := event.(RelayMetricsEvent)
					require.Equal(t, RelayMetricsKind, metricsEvent.Kind)
					expectedRelayId, _ := tag.FromContext(ctx).Value(relayIdTagKey)
					assert.Equal(t, expectedRelayId, metricsEvent.RelayId)
				case <-timeout:
					return
				}
			}
		})
	})
}

func nowInUnixMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
