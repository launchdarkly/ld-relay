package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/events"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

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
		exporter := newOpenCensusEventsExporter(relayId, "envName", publisher, time.Millisecond)
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
			tag.Insert(envNameTagKey, "envName"),
			tag.Insert(platformCategoryTagKey, platformValue),
			tag.Insert(userAgentTagKey, userAgentValue))
		require.NoError(t, err)
		privateConnMetricView := &view.View{
			Measure:     privateConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, envNameTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		privateNewConnMetricView := &view.View{
			Measure:     privateNewConnMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, envNameTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		privatePollingRequestsMeasureView := &view.View{
			Measure:     privatePollingRequestsMeasure,
			Aggregation: view.Sum(),
			TagKeys:     []tag.Key{relayIDTagKey, envNameTagKey, platformCategoryTagKey, userAgentTagKey},
		}
		require.NoError(t, view.Register(privateConnMetricView))
		defer view.Unregister(privateConnMetricView)
		require.NoError(t, view.Register(privateNewConnMetricView))
		defer view.Unregister(privateNewConnMetricView)
		require.NoError(t, view.Register(privatePollingRequestsMeasureView))
		defer view.Unregister(privatePollingRequestsMeasureView)
		f(ctx, exporter, relayId)
	}

	t.Run("exporter generates events", func(*testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		start := ldtime.UnixMillisNow()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			stats.Record(ctx, privateConnMeasure.M(1))
			stats.Record(ctx, privateNewConnMeasure.M(2))
			stats.Record(ctx, privatePollingRequestsMeasure.M(3))

			expectedConn := currentConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Current: 1}
			expectedNewConn := newConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 2}
			expectedPollingMetric := pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 3}
			require.Eventually(t, func() bool {
				metricsEvent := publisher.expectMetricsEvent(t, time.Second)
				mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
				assert.True(t, metricsEvent.StartDate >= start)
				assert.True(t, metricsEvent.StartDate <= metricsEvent.EndDate)
				assert.True(t, metricsEvent.EndDate <= ldtime.UnixMillisNow())
				assert.Equal(t, relayID, metricsEvent.RelayID)
				return len(metricsEvent.Connections) == 1 && metricsEvent.Connections[0] == expectedConn &&
					len(metricsEvent.NewConnections) == 1 && metricsEvent.NewConnections[0] == expectedNewConn &&
					len(metricsEvent.PollingCounts) == 1 && metricsEvent.PollingCounts[0] == expectedPollingMetric
			}, time.Second*5, time.Millisecond*100, "did not receive expected metrics")
		})
	})

	t.Run("polling requests should not be cumulative", func(*testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			stats.Record(ctx, privatePollingRequestsMeasure.M(3))
			expectedPollingMetric := pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 3}
			require.Eventually(t, func() bool {
				metricsEvent := publisher.expectMetricsEvent(t, time.Second)
				mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
				return len(metricsEvent.PollingCounts) == 1 && metricsEvent.PollingCounts[0] == expectedPollingMetric
			}, time.Second*5, time.Millisecond, "did not receive expected metrics")

			stats.Record(ctx, privatePollingRequestsMeasure.M(7))
			expectedPollingMetric = pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 7}
			require.Eventually(t, func() bool {
				metricsEvent := publisher.expectMetricsEvent(t, time.Second)
				mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
				return len(metricsEvent.PollingCounts) == 1 && metricsEvent.PollingCounts[0] == expectedPollingMetric
			}, time.Second*5, time.Millisecond, "did not receive expected metrics")
		})
	})

	t.Run("open connections keep metrics going", func(*testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			stats.Record(ctx, privateConnMeasure.M(1))
			expectedConn := currentConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Current: 1}

			for i := 0; i < 3; i++ {
				require.Eventually(t, func() bool {
					metricsEvent := publisher.expectMetricsEvent(t, time.Second)
					mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)

					return len(metricsEvent.Connections) == 1 && metricsEvent.Connections[0] == expectedConn
				}, time.Second*5, time.Millisecond, "did not receive metrics")
			}

			// This should represent a disconnect of the one streaming connection
			stats.Record(ctx, privateConnMeasure.M(-1))

			// Drain any previously emitted events
			for {
				_, hasEvent := publisher.maybeReceiveMetricsEvent(t, time.Second)
				if !hasEvent {
					break
				}
			}

			// Now we can verify that waiting some amount of time will not
			// result in a new event being emitted
			time.Sleep(time.Millisecond * 50)
			publisher.expectNoMetricsEvent(t, time.Millisecond*50)
		})
	})

	t.Run("empty metrics generate no events", func(*testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			stats.Record(ctx, privateConnMeasure.M(0))
			publisher.expectNoMetricsEvent(t, time.Millisecond*50)
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

			metricsEvent := publisher.expectMetricsEvent(t, time.Second)
			assert.True(t, metricsEvent.StartDate >= startTime)
		})
	})

	t.Run("it ignores metrics for other relays", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			differentRelayID := uuid.New()
			ctxForDifferentRelay, _ := tag.New(ctx, tag.Upsert(relayIDTagKey, differentRelayID))
			stats.Record(ctxForDifferentRelay, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))

			// Any metrics events that we receive should only be for relayID, not differentRelayID
			deadline := time.Now().Add(time.Millisecond * 300)
			for time.Now().Before(deadline) {
				if metricsEvent, ok := publisher.maybeReceiveMetricsEvent(t, deadline.Sub(time.Now())); ok {
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					assert.Equal(t, relayID, metricsEvent.RelayID)
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

			startTime := time.Now()
			for time.Since(startTime) < time.Millisecond*200 {
				metricsEvent := publisher.expectMetricsEvent(t, time.Millisecond*200)
				require.Equal(t, relayMetricsKind, metricsEvent.Kind)
				assert.Equal(t, relayID, metricsEvent.RelayID)
			}
		})
	})

	t.Run("it ignores metrics for other environments", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			differentEnvName := uuid.New()
			ctxForDifferentEnvironment, _ := tag.New(ctx, tag.Upsert(envNameTagKey, differentEnvName))
			stats.Record(ctxForDifferentEnvironment, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))

			// Any metrics events that we receive should only be for relayID, not differentEnvName
			deadline := time.Now().Add(time.Millisecond * 300)
			for time.Now().Before(deadline) {
				if metricsEvent, ok := publisher.maybeReceiveMetricsEvent(t, deadline.Sub(time.Now())); ok {
					require.Equal(t, relayMetricsKind, metricsEvent.Kind)
					assert.Equal(t, relayID, metricsEvent.RelayID)
				}
			}
		})
	})

	t.Run("it ignores metrics that have no environment name", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withTestView(publisher, func(ctx context.Context, exporter *openCensusEventsExporter, relayID string) {
			ctxWithNoEnvName, _ := tag.New(ctx, tag.Delete(envNameTagKey))
			stats.Record(ctxWithNoEnvName, privateConnMeasure.M(1))
			stats.Record(ctx, privateConnMeasure.M(1))

			startTime := time.Now()
			for time.Since(startTime) < time.Millisecond*200 {
				metricsEvent := publisher.expectMetricsEvent(t, time.Millisecond*200)
				require.Equal(t, relayMetricsKind, metricsEvent.Kind)
				assert.Equal(t, relayID, metricsEvent.RelayID)
			}
		})
	})
}
