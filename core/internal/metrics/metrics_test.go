package metrics

import (
	"context"
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/core/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

type args struct {
	measure   Measure
	platform  string
	userAgent string
}

func (a args) getExpectedTags() []tag.Tag {
	return []tag.Tag{tag.Tag{Key: platformCategoryTagKey, Value: a.platform}, tag.Tag{Key: userAgentTagKey, Value: a.userAgent}}
}

type privateMetricsArgs struct {
	args
	relayId string
}

func (a privateMetricsArgs) getExpectedTags() []tag.Tag {
	return append(a.args.getExpectedTags(), tag.Tag{Key: relayIDTagKey, Value: testMetricsRelayID})
}

func TestConnectionMetrics(t *testing.T) {
	specs := []args{
		args{platform: browserTagValue, measure: BrowserConns, userAgent: userAgentValue},
		args{platform: mobileTagValue, measure: MobileConns, userAgent: userAgentValue},
		args{platform: serverTagValue, measure: ServerConns, userAgent: userAgentValue},
	}

	t.Run("public", func(t *testing.T) {
		for _, tt := range specs {
			t.Run(fmt.Sprintf("generates %s connection metrics", tt.platform), func(*testing.T) {
				view.Register(publicConnView)
				defer view.Unregister(publicConnView)
				ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, testMetricsRelayID))
				WithGauge(ctx, userAgentValue, func() {
					expectedTags := tt.getExpectedTags()
					rows, err := view.RetrieveData(publicConnView.Name)
					require.NoError(t, err)
					matchingRows := findRowsWithTags(rows, expectedTags)
					require.Len(t, matchingRows, 1)
					assert.ElementsMatch(t, expectedTags, matchingRows[0].Tags)
					assert.Equal(t, 1, int((*matchingRows[0]).Data.(*view.SumData).Value))
				}, tt.measure)
			})
		}
	})

	t.Run("private", func(t *testing.T) {
		for _, tt := range specs {
			ptt := privateMetricsArgs{args: tt, relayId: testMetricsRelayID}
			t.Run(fmt.Sprintf("generates %s connection metrics", ptt.platform), func(*testing.T) {
				view.Register(privateConnView)
				defer view.Unregister(privateConnView)
				ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, testMetricsRelayID))
				WithGauge(ctx, userAgentValue, func() {
					expectedTags := ptt.getExpectedTags()
					rows, err := view.RetrieveData(privateConnView.Name)
					require.NoError(t, err)
					matchingRows := findRowsWithTags(rows, expectedTags)
					require.Len(t, matchingRows, 1)
					assert.ElementsMatch(t, expectedTags, matchingRows[0].Tags)
					assert.Equal(t, 1, int((*matchingRows[0]).Data.(*view.SumData).Value))
				}, ptt.measure)
			})
		}
	})
}

func TestNewConnectionMetrics(t *testing.T) {
	specs := []args{
		args{platform: browserTagValue, measure: NewBrowserConns, userAgent: userAgentValue},
		args{platform: mobileTagValue, measure: NewMobileConns, userAgent: userAgentValue},
		args{platform: serverTagValue, measure: NewServerConns, userAgent: userAgentValue},
	}

	t.Run("public", func(t *testing.T) {
		for _, tt := range specs {
			t.Run(fmt.Sprintf("generates %s new connection metrics", tt.platform), func(*testing.T) {
				view.Register(publicNewConnView)
				defer view.Unregister(publicNewConnView)
				ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, testMetricsRelayID))
				WithCount(ctx, userAgentValue, func() {
					expectedTags := tt.getExpectedTags()
					rows, err := view.RetrieveData(publicNewConnView.Name)
					require.NoError(t, err)
					matchingRows := findRowsWithTags(rows, expectedTags)
					require.Len(t, matchingRows, 1)
					assert.ElementsMatch(t, expectedTags, matchingRows[0].Tags)
					assert.Equal(t, 1, int((*matchingRows[0]).Data.(*view.SumData).Value))
				}, tt.measure)
			})
		}
	})

	t.Run("private", func(t *testing.T) {
		for _, tt := range specs {
			ptt := privateMetricsArgs{args: tt, relayId: testMetricsRelayID}
			t.Run(fmt.Sprintf("generates %s new connection metrics", ptt.platform), func(*testing.T) {
				view.Register(privateNewConnView)
				defer view.Unregister(privateNewConnView)
				ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, testMetricsRelayID))
				WithCount(ctx, userAgentValue, func() {
					expectedTags := ptt.getExpectedTags()
					rows, err := view.RetrieveData(privateNewConnView.Name)
					require.NoError(t, err)
					matchingRows := findRowsWithTags(rows, expectedTags)
					require.Len(t, matchingRows, 1)
					assert.ElementsMatch(t, expectedTags, matchingRows[0].Tags)
					assert.Equal(t, 1, int((*matchingRows[0]).Data.(*view.SumData).Value))
				}, ptt.measure)
			})
		}
	})
}

func TestWithRouteCount(t *testing.T) {
	type routeArgs struct {
		args
		method string
		route  string
	}

	getExpectedTags := func(a routeArgs) []tag.Tag {
		return append(a.args.getExpectedTags(), tag.Tag{Key: routeTagKey, Value: a.route}, tag.Tag{Key: methodTagKey, Value: a.method})
	}

	exporter := newTestExporter()
	exporterImpl, _ := (&testExporterTypeImpl{instance: exporter}).
		createExporterIfEnabled(config.MetricsConfig{}, ldlog.NewDisabledLoggers())
	_ = exporterImpl.register()
	defer exporterImpl.close()

	view.Register(requestView)
	defer view.Unregister(requestView)

	expected := routeArgs{args: args{platform: serverTagValue, measure: NewServerConns, userAgent: userAgentValue}, method: "GET", route: "someRoute"}

	// Context has a relay Id, but we shouldn't get it back as a tag with public metrics
	ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, testMetricsRelayID))
	WithRouteCount(ctx, userAgentValue, "someRoute", "GET", func() {
		expectedTags := getExpectedTags(expected)
		rows, err := view.RetrieveData(requestView.Name)
		require.NoError(t, err)
		matchingRows := findRowsWithTags(rows, expectedTags)
		require.Len(t, matchingRows, 1)
		assert.ElementsMatch(t, expectedTags, matchingRows[0].Tags)
		assert.Equal(t, 1, int((*matchingRows[0]).Data.(*view.CountData).Value))
	}, ServerRequests)
	assert.NotEmpty(t, exporter.spans)
}

func findRowsWithTags(rows []*view.Row, expectedTags []tag.Tag) (matches []*view.Row) {
RowLoop:
	for _, row := range rows {
		for _, tag := range expectedTags {
			if !contains(row.Tags, tag) {
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
