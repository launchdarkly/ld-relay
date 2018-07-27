package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type args struct {
	value     float64
	measure   Measure
	platform  string
	relayId   string
	userAgent string
}

func (a args) getExpectedTags() []tag.Tag {
	return []tag.Tag{tag.Tag{Key: platformCategoryTagKey, Value: a.platform}, tag.Tag{Key: relayIdTagKey, Value: a.relayId}, tag.Tag{Key: userAgentTagKey, Value: a.userAgent}}
}

type routeArgs struct {
	args
	method string
	route  string
}

func (a routeArgs) getExpectedTags() []tag.Tag {
	return append(a.args.getExpectedTags(), tag.Tag{Key: routeTagKey, Value: a.route}, tag.Tag{Key: methodTagKey, Value: a.method})
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
			expectedTags := tt.getExpectedTags()
			rows, err := view.RetrieveData("connections")
			require.NoError(t, err)
			matchingRows := findRowsWithTags(rows, expectedTags)
			require.Len(t, matchingRows, 1)
			assert.Equal(t, tt.value, float64((*matchingRows[0]).Data.(*view.SumData).Value))
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
			expectedTags := tt.getExpectedTags()
			rows, err := view.RetrieveData("newconnections")
			require.NoError(t, err)
			matchingRows := findRowsWithTags(rows, expectedTags)
			require.Len(t, matchingRows, 1)
			assert.Equal(t, float64((*matchingRows[0]).Data.(*view.SumData).Value), tt.value)
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

	expected := routeArgs{args: args{value: 1, platform: server, measure: NewServerConns, relayId: metricsRelayId, userAgent: userAgentValue}, method: "GET", route: "someRoute"}

	ctx, _ := tag.New(context.Background(), tag.Insert(relayIdTagKey, metricsRelayId))
	WithRouteCount(ctx, userAgentValue, "someRoute", "GET", func() {
		expectedTags := expected.getExpectedTags()
		fmt.Println(expectedTags)
		rows, err := view.RetrieveData("requests")
		require.NoError(t, err)
		matchingRows := findRowsWithTags(rows, expectedTags)
		require.Len(t, matchingRows, 1)
		assert.Equal(t, int64(expected.value), int64((*matchingRows[0]).Data.(*view.CountData).Value))
	}, ServerRequests)
	assert.NotEmpty(t, exporter.spans)
}

func findRowsWithTags(rows []*view.Row, expectedTags []tag.Tag) (matches []*view.Row) {
RowLoop:
	for _, row := range rows {
		if len(row.Tags) != len(expectedTags) {
			continue RowLoop
		}
		for _, tag := range row.Tags {
			if !contains(expectedTags, tag) {
				continue RowLoop
			}
		}
		matches = append(matches, row)
	}
	return matches
}

func contains(tags []tag.Tag, tag tag.Tag) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
