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
	platformValue := "gameConsole"

	withTestView := func(publisher events.EventPublisher, f func(ctx context.Context, exporter *openCensusEventsExporter, relayID string)) {
		relayId := uuid.New()
		exporter := newOpenCensusEventsExporter(relayId, publisher, time.Millisecond)
		defer exporter.close()
		view.RegisterExporter(exporter)
		defer func() {
			view.UnregisterExporter(exporter)
			// Wait for any views to drain
			time.Sleep(testReportingPeriod)
		}()
		ctx, err := tag.New(
			context.Background(),
			tag.Insert(relayIDTagKey, relayId),
			tag.Insert(platformCategoryTagKey, platformValue),
			tag.Insert(userAgentTagKey, userAgentValue))
		require.NoError(t, err)
		privateConnMetricView := &view.View{
			Measure:     privateConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		privateNewConnMetricView := &view.View{
			Measure:     privateNewConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		require.NoError(t, view.Register(privateConnMetricView))
		defer view.Unregister(privateConnMetricView)
		require.NoError(t, view.Register(privateNewConnMetricView))
		defer view.Unregister(privateNewConnMetricView)
		f(ctx, exporter, relayId)
	}

	t.Run("exporter generates events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		start := ldtime.UnixMillisNow()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			stats.Record(ctx, privateConnMeasure.M(1))
			stats.Record(ctx, privateNewConnMeasure.M(2))
			expectedConn := currentConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Current: 1}
			expectedNewConn := newConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 2}
			var event interface{}
			timeout := time.After(time.Second)
		EventLoop:
			for {
				select {
				case event = <-publisher.events:
					require.IsType(t, relayMetricsEvent{}, event)
					metricsEvent := event.(relayMetricsEvent)
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					assert.True(t, metricsEvent.StartDate >= start)
					assert.True(t, metricsEvent.StartDate <= metricsEvent.EndDate)
					assert.True(t, metricsEvent.EndDate <= ldtime.UnixMillisNow())
					assert.Equal(t, relayID, metricsEvent.RelayID)
					if len(metricsEvent.Connections) == 1 && metricsEvent.Connections[0] == expectedConn &&
						len(metricsEvent.NewConnections) == 1 && metricsEvent.NewConnections[0] == expectedNewConn {
						break EventLoop
					}
				case <-timeout:
					require.Fail(t, "timed out", "last event received: %+v", event)
					break EventLoop
				}
			}
		})
	})

	t.Run("empty metrics generate no events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
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
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
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

	t.Run("it ignores metrics for other relays", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			ctxForDifferentRelay, _ := tag.New(ctx, tag.Upsert(relayIDTagKey, uuid.New()))
			stats.Record(ctxForDifferentRelay, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))
			timeout := time.After(time.Millisecond * 200)
			for {
				select {
				case event := <-publisher.events:
					metricsEvent := event.(relayMetricsEvent)
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					assert.Equal(t, relayID, metricsEvent.RelayID)
				case <-timeout:
					return
				}
			}
		})
	})

	t.Run("it ignores metrics that have no relay ID", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			ctxWithNoRelayID, _ := tag.New(ctx, tag.Delete(relayIDTagKey))
			stats.Record(ctxWithNoRelayID, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))
			timeout := time.After(time.Millisecond * 200)
			for {
				select {
				case event := <-publisher.events:
					metricsEvent := event.(relayMetricsEvent)
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					assert.Equal(t, relayID, metricsEvent.RelayID)
				case <-timeout:
					return
				}
			}
		})
	})
}
