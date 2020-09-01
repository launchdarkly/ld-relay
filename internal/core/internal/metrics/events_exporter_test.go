package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/events"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"
)

const testReportingPeriod = time.Millisecond

func init() {
	view.SetReportingPeriod(testReportingPeriod)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
}

func TestOpenCensusEventsExporter(t *testing.T) {
	withTestView := func(publisher events.EventPublisher, f func(ctx context.Context, exporter *openCensusEventsExporter)) {
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
			tag.Insert(relayIDTagKey, relayId),
			tag.Insert(platformCategoryTagKey, "gameConsole"),
			tag.Insert(userAgentTagKey, "my-agent"))
		require.NoError(t, err)
		metricView := &view.View{
			Measure:     privateConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		require.NoError(t, view.Register(metricView))
		defer view.Unregister(metricView)
		f(ctx, exporter)
	}

	t.Run("exporter generates events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		start := ldtime.UnixMillisNow()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter) {
			stats.Record(ctx, privateConnMeasure.M(1))
			var event interface{}
			select {
			case event = <-publisher.events:
				break
			case <-time.After(time.Second):
				require.Fail(t, "timed out")
			}
			require.IsType(t, relayMetricsEvent{}, event)
			metricsEvent := event.(relayMetricsEvent)
			require.Equal(t, relayMetricsKind, metricsEvent.Kind)
			assert.True(t, metricsEvent.StartDate >= start)
			assert.True(t, metricsEvent.StartDate <= metricsEvent.EndDate)
			assert.True(t, metricsEvent.EndDate <= ldtime.UnixMillisNow())
			expectedRelayId, _ := tag.FromContext(ctx).Value(relayIDTagKey)
			assert.Equal(t, expectedRelayId, metricsEvent.RelayID)
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
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter) {
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
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter) {
			time.Sleep(time.Millisecond * 10)
			startTime := ldtime.UnixMillisNow()
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
			require.IsType(t, relayMetricsEvent{}, event)
			metricsEvent := event.(relayMetricsEvent)
			assert.True(t, metricsEvent.StartDate >= startTime)
		})
	})

	t.Run("it ignores metrics for other relays", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter) {
			ctxForDifferentRelay, _ := tag.New(ctx, tag.Upsert(relayIDTagKey, uuid.New()))
			stats.Record(ctxForDifferentRelay, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))
			timeout := time.After(time.Second)
			for {
				select {
				case event := <-publisher.events:
					metricsEvent := event.(relayMetricsEvent)
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					expectedRelayId, _ := tag.FromContext(ctx).Value(relayIDTagKey)
					assert.Equal(t, expectedRelayId, metricsEvent.RelayID)
				case <-timeout:
					return
				}
			}
		})
	})
}
