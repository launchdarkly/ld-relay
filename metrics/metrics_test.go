package metrics

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opencensus.io/stats/view"
)

type testEventsPublisher struct {
	events chan interface{}
}

func newTestEventsPublisher() *testEventsPublisher {
	return &testEventsPublisher{
		events: make(chan interface{}, 100),
	}
}

func (p *testEventsPublisher) Publish(events ...interface{}) {
	for _, e := range events {
		p.events <- e
	}
}
func (p *testEventsPublisher) PublishRaw(events ...json.RawMessage) {}
func (p *testEventsPublisher) Flush()                               {}

func init() {
	view.SetReportingPeriod(time.Millisecond)
}

func TestConnectionMetrics(t *testing.T) {
	t.Parallel()
	specs := []struct {
		expected         interface{}
		platformCategory string
		measure          Measure
	}{
		{
			currentConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: browser,
				Current:          1,
			},
			browser,
			BrowserConns,
		},
		{
			currentConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: mobile,
				Current:          1,
			},
			mobile,
			MobileConns,
		},
		{
			currentConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: server,
				Current:          1,
			},
			server,
			ServerConns,
		},
	}
	for _, tt := range specs {
		t.Run(fmt.Sprintf("%s_%s", tt.platformCategory, tt.measure.measure.Name()), func(t *testing.T) {
			publisher := newTestEventsPublisher()
			p, err := NewMetricsProcessor(publisher, OptionFlushInterval(time.Millisecond))
			if !assert.NoError(t, err) {
				return
			}
			defer p.Close()
			WithGauge(p.OpenCensusCtx, "my-agent", func() {
				var event interface{}
				select {
				case event = <-publisher.events:
				case <-time.After(time.Second):
					assert.Fail(t, "timed out")
					return
				}
				if !assert.IsType(t, RelayMetricsEvent{}, event) {
					return
				}
				metricsEvent := event.(RelayMetricsEvent)
				assert.Equal(t, RelayMetricsKind, metricsEvent.Kind)
				if !assert.ElementsMatch(t, []currentConnectionsMetric{tt.expected.(currentConnectionsMetric)}, metricsEvent.Connections) {
					t.Logf("Received events were: %+v", metricsEvent.Connections)
				}
			}, tt.measure)
		})
	}
}

func TestNewConnectionMetrics(t *testing.T) {
	t.Parallel()
	specs := []struct {
		expected         interface{}
		platformCategory string
		measure          Measure
	}{
		{
			newConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: browser,
				Count:            1,
			},
			browser,
			NewBrowserConns,
		},
		{
			newConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: mobile,
				Count:            1,
			},
			mobile,
			NewMobileConns,
		},
		{
			newConnectionsMetric{
				UserAgent:        "my-agent",
				PlatformCategory: server,
				Count:            1,
			},
			server,
			NewServerConns,
		}}
	for _, tt := range specs {
		t.Run(fmt.Sprintf("%s_%s", tt.platformCategory, tt.measure.measure.Name()), func(t *testing.T) {
			publisher := newTestEventsPublisher()
			p, err := NewMetricsProcessor(publisher, OptionFlushInterval(time.Millisecond))
			if !assert.NoError(t, err) {
				return
			}
			defer p.Close()
			WithCount(p.OpenCensusCtx, "my-agent", func() {
				var event interface{}
				select {
				case event = <-publisher.events:
				case <-time.After(time.Second):
					assert.Fail(t, "timed out")
					return
				}
				if !assert.IsType(t, RelayMetricsEvent{}, event) {
					return
				}
				metricsEvent := event.(RelayMetricsEvent)
				assert.Equal(t, RelayMetricsKind, metricsEvent.Kind)
				if !assert.ElementsMatch(t, []newConnectionsMetric{tt.expected.(newConnectionsMetric)}, metricsEvent.NewConnections) {
					t.Logf("Received events were: %+v", metricsEvent.NewConnections)
				}
			}, tt.measure)
		})
	}
}
