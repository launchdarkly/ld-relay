package metrics

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
)

type currentConnectionsMetric struct {
	UserAgent        string `json:"userAgent"`
	SDKWrapper       string `json:"sdkWrapper"`
	PlatformCategory string `json:"platformCategory"`
	Current          int64  `json:"current"`
}

type pollingMetric struct {
	UserAgent        string `json:"userAgent"`
	SDKWrapper       string `json:"sdkWrapper"`
	PlatformCategory string `json:"platformCategory"`
	Count            int64  `json:"count"`
}

const relayMetricsKind = "relayMetrics"

type relayMetricsEvent struct {
	Kind          string                     `json:"kind"`
	RelayID       string                     `json:"relayId"`
	StartDate     ldtime.UnixMillisecondTime `json:"startDate"`
	EndDate       ldtime.UnixMillisecondTime `json:"endDate"`
	Connections   []currentConnectionsMetric `json:"connections,omitempty"`
	PollingCounts []pollingMetric            `json:"pollingCounts,omitempty"`
}

type connectionsKeyType struct {
	userAgent        string
	sdkWrapper       string
	platformCategory string
}

type pollingCounts struct {
	lastReported int64
	running      int64
}

// RelayMetricsCollector collects connection and polling metrics for a single environment
// and periodically publishes them as relayMetrics events. Unlike the old OpenCensus-based
// events exporter, this collector is called directly from recording functions rather than
// receiving data through the OpenCensus view export pipeline.
type RelayMetricsCollector struct {
	relayID           string
	envName           string
	publisher         events.EventPublisher
	logger            *slog.Logger
	intervalStartTime time.Time

	currentConnections map[connectionsKeyType]int64

	pollingDataIsDirty bool
	pollingCounts      map[connectionsKeyType]pollingCounts
	mu                 sync.Mutex
	closer             chan struct{}
}

func newRelayMetricsCollector(relayID, envName string, publisher events.EventPublisher, flushInterval time.Duration, logger *slog.Logger) *RelayMetricsCollector {
	c := &RelayMetricsCollector{
		relayID:            relayID,
		envName:            envName,
		publisher:          publisher,
		logger:             logger,
		closer:             make(chan struct{}),
		intervalStartTime:  time.Now(),
		pollingDataIsDirty: false,
		currentConnections: make(map[connectionsKeyType]int64),
		pollingCounts:      make(map[connectionsKeyType]pollingCounts),
	}

	flushTicker := time.NewTicker(flushInterval)

	go func() {
	FlushLoop:
		for {
			select {
			case <-flushTicker.C:
				c.flush()
			case <-c.closer:
				break FlushLoop
			}
		}
		flushTicker.Stop()
		c.flush()
	}()

	return c
}

// RecordConnectionChange records a change in the number of active connections.
// Use delta=1 for a new connection and delta=-1 for a disconnection.
func (c *RelayMetricsCollector) RecordConnectionChange(platform, userAgent, sdkWrapper string, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connectionsKeyType{platformCategory: platform, userAgent: userAgent, sdkWrapper: sdkWrapper}
	newVal := c.currentConnections[key] + delta
	if newVal == 0 {
		delete(c.currentConnections, key)
	} else {
		c.currentConnections[key] = newVal
	}
}

// RecordPollingRequest records a polling request.
func (c *RelayMetricsCollector) RecordPollingRequest(platform, userAgent, sdkWrapper string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connectionsKeyType{platformCategory: platform, userAgent: userAgent, sdkWrapper: sdkWrapper}
	counts := c.pollingCounts[key]
	counts.running++
	c.pollingCounts[key] = counts
	c.pollingDataIsDirty = true
}

func (c *RelayMetricsCollector) hasMetricDataToReport() bool {
	if c.pollingDataIsDirty {
		return true
	}
	if len(c.currentConnections) > 0 {
		return true
	}
	return false
}

func (c *RelayMetricsCollector) flush() {
	c.mu.Lock()
	startTime := c.intervalStartTime
	stopTime := time.Now()
	c.intervalStartTime = stopTime

	if !c.hasMetricDataToReport() {
		c.mu.Unlock()
		return
	}

	event := relayMetricsEvent{
		Kind:      relayMetricsKind,
		RelayID:   c.relayID,
		StartDate: ldtime.UnixMillisFromTime(startTime),
		EndDate:   ldtime.UnixMillisFromTime(stopTime),
	}

	if c.pollingDataIsDirty {
		for k, v := range c.pollingCounts {
			if v.running != v.lastReported {
				event.PollingCounts = append(event.PollingCounts, pollingMetric{
					UserAgent:        k.userAgent,
					SDKWrapper:       k.sdkWrapper,
					PlatformCategory: k.platformCategory,
					Count:            v.running - v.lastReported,
				})
				v.lastReported = v.running
				c.pollingCounts[k] = v
			}
		}
		c.pollingDataIsDirty = false
	}
	for k, v := range c.currentConnections {
		event.Connections = append(event.Connections, currentConnectionsMetric{
			UserAgent:        k.userAgent,
			SDKWrapper:       k.sdkWrapper,
			PlatformCategory: k.platformCategory,
			Current:          v,
		})
	}
	c.mu.Unlock()

	jsonData, err := json.Marshal(event)
	if err != nil {
		c.logger.Error("failed to marshal relay metrics event", "error", err)
		return
	}
	c.publisher.Publish(events.EventPayloadMetadata{}, jsonData)
}

func (c *RelayMetricsCollector) close() {
	close(c.closer)
}
