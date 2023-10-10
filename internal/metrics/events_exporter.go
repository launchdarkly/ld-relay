package metrics

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/events"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"go.opencensus.io/stats/view"
)

type currentConnectionsMetric struct {
	UserAgent        string `json:"userAgent"`
	PlatformCategory string `json:"platformCategory"`
	Current          int64  `json:"current"`
}

type newConnectionsMetric struct {
	UserAgent        string `json:"userAgent"`
	PlatformCategory string `json:"platformCategory"`
	Count            int64  `json:"count"`
}

type pollingMetric struct {
	UserAgent        string `json:"userAgent"`
	PlatformCategory string `json:"platformCategory"`
	Count            int64  `json:"count"`
}

const relayMetricsKind = "relayMetrics"

type relayMetricsEvent struct {
	Kind           string                     `json:"kind"`
	RelayID        string                     `json:"relayId"`
	StartDate      ldtime.UnixMillisecondTime `json:"startDate"`
	EndDate        ldtime.UnixMillisecondTime `json:"endDate"`
	Connections    []currentConnectionsMetric `json:"connections,omitempty"`
	NewConnections []newConnectionsMetric     `json:"newConnections,omitempty"`
	PollingCounts  []pollingMetric            `json:"pollingCounts,omitempty"`
}

type connectionsKeyType struct {
	userAgent        string
	platformCategory string
}

type pollingCounts struct {
	lastReported int64
	running      int64
}

// The openCensusEventsExporter is used for publishing connection statistics to the LaunchDarkly events service.
type openCensusEventsExporter struct {
	relayID           string
	envName           string
	publisher         events.EventPublisher
	intervalStartTime time.Time

	currentConnections map[connectionsKeyType]int64
	newConnections     map[connectionsKeyType]int64

	// Convenience flag to determine if the pollingCounts should report new data.
	pollingDataIsDirty bool
	pollingCounts      map[connectionsKeyType]pollingCounts
	mu                 sync.Mutex
	closer             chan<- struct{}
}

func newOpenCensusEventsExporter(relayID, envName string, publisher events.EventPublisher, flushInterval time.Duration) *openCensusEventsExporter {
	closer := make(chan struct{})

	e := &openCensusEventsExporter{
		relayID:            relayID,
		envName:            envName,
		publisher:          publisher,
		closer:             closer,
		intervalStartTime:  time.Now(),
		pollingDataIsDirty: false,
		currentConnections: make(map[connectionsKeyType]int64),
		newConnections:     make(map[connectionsKeyType]int64),
		pollingCounts:      make(map[connectionsKeyType]pollingCounts),
	}

	flushTicker := time.NewTicker(flushInterval)

	go func() {
	FlushLoop:
		for {
			select {
			case <-flushTicker.C:
				e.flush()
			case <-closer:
				break FlushLoop
			}
		}
		flushTicker.Stop()
	}()

	return e
}

func (e *openCensusEventsExporter) ExportView(viewData *view.Data) {
	if viewData != nil && viewData.View != nil {
	NextRow:
		for _, r := range viewData.Rows {
			var platformCategory string
			var userAgent string
			relayIDFound := false
			envNameFound := false
			for _, t := range r.Tags {
				switch t.Key {
				case relayIDTagKey:
					if t.Value == e.relayID {
						relayIDFound = true
					} else {
						continue NextRow
					}
				case envNameTagKey:
					if t.Value == e.envName {
						envNameFound = true
					} else {
						continue NextRow
					}
				case userAgentTagKey:
					userAgent = t.Value
				case platformCategoryTagKey:
					platformCategory = t.Value
				}
			}
			if !relayIDFound || !envNameFound {
				continue NextRow
			}
			var v int64
			if data, ok := r.Data.(*view.SumData); ok {
				v = int64(data.Value)
			}
			e.updateValue(viewData.View.Name, platformCategory, userAgent, v)
		}
	}
}

func (e *openCensusEventsExporter) updateValue(name string, platformCategory string, userAgent string, value int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch name {
	case privatePollingRequestsMeasureName:
		key := connectionsKeyType{platformCategory: platformCategory, userAgent: userAgent}
		if value == 0 {
			delete(e.pollingCounts, key)
			break
		}

		counts := e.pollingCounts[key]
		if counts.running != value {
			counts.running = value
			e.pollingCounts[key] = counts
			e.pollingDataIsDirty = true
		}

	case privateConnMeasureName:
		key := connectionsKeyType{platformCategory: platformCategory, userAgent: userAgent}
		if value == 0 {
			delete(e.currentConnections, key)
		} else {
			e.currentConnections[key] = value
		}

	case privateNewConnMeasureName:
		key := connectionsKeyType{platformCategory: platformCategory, userAgent: userAgent}
		if value == 0 {
			delete(e.newConnections, key) // COVERAGE: won't happen in practice since this measure is only ever incremented
		} else {
			e.newConnections[key] = value
		}
	}
}

// hasMetricDataToReport will return true if there is data currently being
// tracked by that exporter that should be returned into a relayMetrics event.
//
// WARN: This method does not lock the underlying mutex. It is expected that
// you will have done so before calling this method.
func (e *openCensusEventsExporter) hasMetricDataToReport() bool {
	if e.pollingDataIsDirty {
		return true
	}

	if len(e.currentConnections) > 0 {
		return true
	}

	if len(e.newConnections) > 0 {
		return true
	}

	return false
}

func (e *openCensusEventsExporter) flush() {
	e.mu.Lock()
	startTime := e.intervalStartTime
	stopTime := time.Now()
	e.intervalStartTime = stopTime

	if !e.hasMetricDataToReport() {
		e.mu.Unlock()
		return
	}

	event := relayMetricsEvent{
		Kind:      relayMetricsKind,
		RelayID:   e.relayID,
		StartDate: ldtime.UnixMillisFromTime(startTime),
		EndDate:   ldtime.UnixMillisFromTime(stopTime),
	}

	if e.pollingDataIsDirty {
		for k, v := range e.pollingCounts {
			// Polling counts aren't reset between flushes because we need to track
			// the offset between flushes. So it is possible some counts haven't
			// changed, so there is no point in sending them.
			if v.running != v.lastReported {
				event.PollingCounts = append(event.PollingCounts, pollingMetric{
					UserAgent:        k.userAgent,
					PlatformCategory: k.platformCategory,
					Count:            v.running - v.lastReported,
				})
				v.lastReported = v.running
				e.pollingCounts[k] = v
			}
		}
		e.pollingDataIsDirty = false
	}
	for k, v := range e.currentConnections {
		event.Connections = append(event.Connections, currentConnectionsMetric{
			UserAgent:        k.userAgent,
			PlatformCategory: k.platformCategory,
			Current:          v,
		})
	}
	for k, v := range e.newConnections {
		event.NewConnections = append(event.NewConnections, newConnectionsMetric{
			UserAgent:        k.userAgent,
			PlatformCategory: k.platformCategory,
			Count:            v,
		})
	}
	e.newConnections = make(map[connectionsKeyType]int64, len(e.newConnections))
	e.mu.Unlock()

	json, _ := json.Marshal(event)
	e.publisher.Publish(events.EventPayloadMetadata{}, json)
}

func (e *openCensusEventsExporter) close() {
	close(e.closer)
}
