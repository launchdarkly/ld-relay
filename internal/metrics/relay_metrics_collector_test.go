package metrics

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCollectorFlushInterval = time.Millisecond

func TestRelayMetricsCollector(t *testing.T) {
	platformValue := "gameConsole"

	withCollector := func(publisher *testEventsPublisher, f func(c *RelayMetricsCollector, relayID string)) {
		relayID := uuid.New()
		mockLog := ldlogtest.NewMockLog()
		c := newRelayMetricsCollector(relayID, "envName", publisher, testCollectorFlushInterval, mockLog.Loggers)
		defer c.close()
		f(c, relayID)
	}

	t.Run("collector generates events", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		start := ldtime.UnixMillisNow()
		withCollector(publisher, func(c *RelayMetricsCollector, relayID string) {
			c.RecordConnectionChange(platformValue, userAgentValue, "", 1)
			c.RecordNewConnection(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")

			expectedConn := currentConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Current: 1}
			expectedNewConn := newConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 1}
			expectedPollingMetric := pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 1}

			require.Eventually(t, func() bool {
				c.flush()
				metricsEvent, ok := publisher.maybeReceiveMetricsEvent(t, time.Millisecond*100)
				if !ok {
					return false
				}
				mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
				assert.True(t, metricsEvent.StartDate >= start)
				assert.True(t, metricsEvent.StartDate <= metricsEvent.EndDate)
				assert.True(t, metricsEvent.EndDate <= ldtime.UnixMillisNow())
				assert.Equal(t, relayID, metricsEvent.RelayID)
				return len(metricsEvent.Connections) == 1 && metricsEvent.Connections[0] == expectedConn &&
					len(metricsEvent.NewConnections) == 1 && metricsEvent.NewConnections[0] == expectedNewConn &&
					len(metricsEvent.PollingCounts) == 1 && metricsEvent.PollingCounts[0] == expectedPollingMetric
			}, time.Second*5, time.Millisecond*10, "did not receive expected metrics")
		})
	})

	t.Run("polling requests should not be cumulative across flushes", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		withCollector(publisher, func(c *RelayMetricsCollector, relayID string) {
			c.RecordPollingRequest(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")

			c.flush()
			expectedPollingMetric := pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 3}
			metricsEvent := publisher.expectMetricsEvent(t, time.Second)
			mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
			require.Len(t, metricsEvent.PollingCounts, 1)
			assert.Equal(t, expectedPollingMetric, metricsEvent.PollingCounts[0])

			// Record more polling requests
			c.RecordPollingRequest(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")
			c.RecordPollingRequest(platformValue, userAgentValue, "")

			c.flush()
			expectedPollingMetric = pollingMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Count: 4}
			metricsEvent = publisher.expectMetricsEvent(t, time.Second)
			mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
			require.Len(t, metricsEvent.PollingCounts, 1)
			assert.Equal(t, expectedPollingMetric, metricsEvent.PollingCounts[0])
		})
	})

	t.Run("open connections keep metrics going", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		publisher := newTestEventsPublisher()
		withCollector(publisher, func(c *RelayMetricsCollector, relayID string) {
			c.RecordConnectionChange(platformValue, userAgentValue, "", 1)
			expectedConn := currentConnectionsMetric{UserAgent: userAgentValue, PlatformCategory: platformValue, Current: 1}

			for i := 0; i < 3; i++ {
				c.flush()
				metricsEvent := publisher.expectMetricsEvent(t, time.Second)
				mockLog.Loggers.Infof("received metrics: %+v", metricsEvent)
				require.Len(t, metricsEvent.Connections, 1)
				assert.Equal(t, expectedConn, metricsEvent.Connections[0])
			}

			// Disconnect
			c.RecordConnectionChange(platformValue, userAgentValue, "", -1)
			c.flush()

			// Drain any remaining events
			for {
				_, hasEvent := publisher.maybeReceiveMetricsEvent(t, time.Millisecond*50)
				if !hasEvent {
					break
				}
			}

			// Now verify that no more events are emitted
			c.flush()
			publisher.expectNoMetricsEvent(t, time.Millisecond*50)
		})
	})

	t.Run("empty metrics generate no events", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withCollector(publisher, func(c *RelayMetricsCollector, relayID string) {
			c.flush()
			publisher.expectNoMetricsEvent(t, time.Millisecond*50)
		})
	})

	t.Run("the event start time still shifts when events are not sent", func(t *testing.T) {
		publisher := newTestEventsPublisher()
		withCollector(publisher, func(c *RelayMetricsCollector, relayID string) {
			time.Sleep(time.Millisecond * 10)
			startTime := ldtime.UnixMillisNow()
			time.Sleep(time.Millisecond * 1)
			c.RecordConnectionChange(platformValue, userAgentValue, "", 1)

			c.flush()
			metricsEvent := publisher.expectMetricsEvent(t, time.Second)
			assert.True(t, metricsEvent.StartDate >= startTime)
		})
	})
}
