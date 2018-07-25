package metrics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"
)

var (
	userAgentValue = "my-agent"
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
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
}

type args struct {
	value     float64
	measure   Measure
	platform  string
	relayId   string
	userAgent string
	method    string
	route     string
}

func TestConnectionMetrics(t *testing.T) {
	specs := []args{
		args{value: 1, platform: browser, measure: BrowserConns, relayId: metricsRelayId, userAgent: userAgentValue},
		args{value: 1, platform: mobile, measure: MobileConns, relayId: metricsRelayId, userAgent: userAgentValue},
		args{value: 1, platform: server, measure: ServerConns, relayId: metricsRelayId, userAgent: userAgentValue},
	}

	for _, tt := range specs {
		publisher := newTestEventsPublisher()
		p, err := NewMetricsProcessor(publisher, OptionFlushInterval(time.Millisecond))
		if !assert.NoError(t, err) {
			return
		}
		defer p.Close()
		WithGauge(p.OpenCensusCtx, userAgentValue, func() {
			equalCheck(t, tt, "connections")
		}, tt.measure)

	}
}

func TestNewConnectionMetrics(t *testing.T) {
	specs := []args{
		args{value: 1, platform: browser, measure: NewBrowserConns, relayId: metricsRelayId, userAgent: userAgentValue},
		args{value: 1, platform: mobile, measure: NewMobileConns, relayId: metricsRelayId, userAgent: userAgentValue},
		args{value: 1, platform: server, measure: NewServerConns, relayId: metricsRelayId, userAgent: userAgentValue},
	}

	for _, tt := range specs {
		publisher := newTestEventsPublisher()
		p, err := NewMetricsProcessor(publisher, OptionFlushInterval(time.Millisecond))
		if !assert.NoError(t, err) {
			return
		}
		defer p.Close()
		WithCount(p.OpenCensusCtx, userAgentValue, func() {
			equalCheck(t, tt, "newconnections")
		}, tt.measure)
	}
}

type testExporter struct {
	spans chan *trace.SpanData
}

func (e *testExporter) ExportSpan(s *trace.SpanData) {
	e.spans <- s
}

func newTestExporter() *testExporter {
	return &testExporter{spans: make(chan *trace.SpanData, 100)}
}

const testExporterType ExporterType = "test"

type TestOptions int

func (t TestOptions) getType() ExporterType {
	return testExporterType
}

func TestExporterRegisterersAreInited(t *testing.T) {
	assert.Equal(t, 3, len(exporters))
}

func TestWithRouteCount(t *testing.T) {
	exporter := newTestExporter()
	defineExporter(testExporterType, func(o ExporterOptions) error {
		trace.RegisterExporter(exporter)
		return nil
	})

	if !assert.NoError(t, RegisterExporters([]ExporterOptions{TestOptions(0)})) {
		return
	}
	defer trace.UnregisterExporter(exporter)
	defer view.Unregister(&view.View{Name: "requests"})

	expected := args{value: 1, platform: server, measure: NewServerConns, userAgent: userAgentValue, method: "GET", route: "someRoute"}

	WithRouteCount(context.Background(), userAgentValue, "someRoute", "GET", func() {
		routeEqualCheck(t, expected, "requests")
	}, ServerRequests)
	assert.NotEmpty(t, exporter.spans)
}

func routeEqualCheck(t *testing.T, expected args, viewName string) {
	expectedRow := &view.Row{Tags: []tag.Tag{tag.Tag{Key: methodTagKey, Value: expected.method}, tag.Tag{Key: platformCategoryTagKey, Value: expected.platform}, tag.Tag{Key: routeTagKey, Value: expected.route}, tag.Tag{Key: userAgentTagKey, Value: expected.userAgent}}, Data: &view.CountData{Value: int64(expected.value)}}
	rowEqualCheck(t, expectedRow, viewName)
}

func equalCheck(t *testing.T, expected args, viewName string) {
	expectedRow := &view.Row{Tags: []tag.Tag{tag.Tag{Key: platformCategoryTagKey, Value: expected.platform}, tag.Tag{Key: relayIdTagKey, Value: expected.relayId}, tag.Tag{Key: userAgentTagKey, Value: expected.userAgent}}, Data: &view.SumData{Value: expected.value}}
	rowEqualCheck(t, expectedRow, viewName)
}

func rowEqualCheck(t *testing.T, expected *view.Row, viewName string) {
	rows, _ := view.RetrieveData(viewName)

	found := false
EqualCheck:
	for _, row := range rows {
		if expected.Equal(row) {
			found = true
			break EqualCheck
		}
	}
	if !assert.True(t, found) {
		t.Logf("%+v does not contain\n%+v", rows, expected)
	}
}
