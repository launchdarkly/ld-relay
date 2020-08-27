package metrics

import (
	"sync"
	"time"

	"go.opencensus.io/stats/view"

	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
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

const relayMetricsKind = "relayMetrics"

type relayMetricsEvent struct {
	Kind           string                     `json:"kind"`
	RelayID        string                     `json:"relayId"`
	StartDate      int64                      `json:"startDate"`
	EndDate        int64                      `json:"endDate"`
	Connections    []currentConnectionsMetric `json:"connections,omitempty"`
	NewConnections []newConnectionsMetric     `json:"newConnections,omitempty"`
}

type connectionsKeyType struct {
	userAgent        string
	platformCategory string
}

type openCensusEventsExporter struct {
	relayID            string
	publisher          events.EventPublisher
	intervalStartTime  time.Time
	currentConnections map[connectionsKeyType]int64
	newConnections     map[connectionsKeyType]int64
	mu                 sync.Mutex
	closer             chan<- struct{}
}

func newopenCensusEventsExporter(relayID string, publisher events.EventPublisher, flushInterval time.Duration) *openCensusEventsExporter {
	closer := make(chan struct{})

	e := &openCensusEventsExporter{
		relayID:            relayID,
		publisher:          publisher,
		closer:             closer,
		intervalStartTime:  time.Now(),
		currentConnections: make(map[connectionsKeyType]int64),
		newConnections:     make(map[connectionsKeyType]int64),
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
			for _, t := range r.Tags {
				switch t.Key {
				case relayIDTagKey:
					if t.Value == e.relayID {
						relayIDFound = true
					} else {
						continue NextRow
					}
				case userAgentTagKey:
					userAgent = t.Value
				case platformCategoryTagKey:
					platformCategory = t.Value
				}
			}
			if !relayIDFound {
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
			delete(e.newConnections, key)
		} else {
			e.newConnections[key] = value
		}
	}
}

func (e *openCensusEventsExporter) flush() {
	e.mu.Lock()
	startTime := e.intervalStartTime
	stopTime := time.Now()
	e.intervalStartTime = stopTime
	if len(e.currentConnections) == 0 && len(e.newConnections) == 0 {
		e.mu.Unlock()
		return
	}
	event := relayMetricsEvent{
		Kind:      relayMetricsKind,
		RelayID:   e.relayID,
		StartDate: unixMillis(startTime),
		EndDate:   unixMillis(stopTime),
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
	e.publisher.Publish(event)
}

func (e *openCensusEventsExporter) close() {
	close(e.closer)
}

func unixMillis(t time.Time) int64 {
	return t.UnixNano() / int64(time.Millisecond)
}
