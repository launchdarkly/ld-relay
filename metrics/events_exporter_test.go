package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"

	"gopkg.in/launchdarkly/ld-relay.v5/events"
)

func TestOpenCensusEventsExporter(t *testing.T) {
	t.Parallel()
	withTestView := func(publisher events.EventPublisher, f func(ctx context.Context, exporter *OpenCensusEventsExporter)) {
		relayId := uuid.New()
		exporter := newOpenCensusEventsExporter(relayId, publisher, time.Millisecond)
		view.RegisterExporter(exporter)
		defer view.UnregisterExporter(exporter)
		ctx, err := tag.New(
			context.Background(),
			tag.Insert(relayIdTagKey, relayId),
			tag.Insert(platformCategoryTagKey, "gameConsole"),
			tag.Insert(userAgentTagKey, "my-agent"))
		assert.NoError(t, err)
		metricView := &view.View{
			Measure:     connMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIdTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		assert.NoError(t, view.Register(metricView))
		defer view.Unregister(metricView)
		f(ctx, exporter)
	}

	t.Run("exporter generates events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		start := nowInUnixMillis()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			stats.Record(ctx, BrowserConns.measure.M(1))
			var event interface{}
			select {
			case event = <-publisher.events:
				break
			case <-time.After(time.Second):
				assert.Fail(t, "timed out")
				return
			}
			if !assert.IsType(t, RelayMetricsEvent{}, event) {
				return
			}
			metricsEvent := event.(RelayMetricsEvent)
			assert.Equal(t, RelayMetricsKind, metricsEvent.Kind)
			assert.True(t, metricsEvent.StartDate >= start/int64(time.Millisecond))
			assert.True(t, metricsEvent.StartDate <= metricsEvent.StopDate)
			assert.True(t, metricsEvent.StopDate <= nowInUnixMillis())
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
			stats.Record(ctx, BrowserConns.measure.M(0))
			select {
			case event := <-publisher.events:
				assert.Fail(t, "expected no events", "got one: %+v", event)
			case <-time.After(time.Second):
				return
			}
		})
	})

	t.Run("the event start time still shifts when events are not sent", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			time.Sleep(time.Millisecond * 10)
			startTime := nowInUnixMillis()
			stats.Record(ctx, BrowserConns.measure.M(1))
			var event interface{}
			select {
			case event = <-publisher.events:
				break
			case <-time.After(time.Second):
				assert.Fail(t, "timed out")
				return
			}
			if !assert.IsType(t, RelayMetricsEvent{}, event) {
				return
			}
			metricsEvent := event.(RelayMetricsEvent)
			assert.True(t, metricsEvent.StartDate >= startTime)
		})
	})

	t.Run("it ignores metrics for other relays", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *OpenCensusEventsExporter) {
			ctxForDifferentRelay, _ := tag.New(ctx, tag.Upsert(relayIdTagKey, uuid.New()))
			stats.Record(ctxForDifferentRelay, BrowserConns.measure.M(1))
			stats.Record(ctx, BrowserConns.measure.M(1))
			timeout := time.After(time.Second)
			for {
				select {
				case event := <-publisher.events:
					metricsEvent := event.(RelayMetricsEvent)
					if assert.Equal(t, RelayMetricsKind, metricsEvent.Kind) {
						expectedRelayId, _ := tag.FromContext(ctx).Value(relayIdTagKey)
						assert.Equal(t, expectedRelayId, metricsEvent.RelayId)
					}
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
