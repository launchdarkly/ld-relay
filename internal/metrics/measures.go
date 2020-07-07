package metrics

import (
	"context"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	"github.com/launchdarkly/ld-relay/v6/internal/logging"
)

var (
	BrowserConns = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &browserTags}
	MobileConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &mobileTags}
	ServerConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &serverTags}

	NewBrowserConns = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &browserTags}
	NewMobileConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &mobileTags}
	NewServerConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &serverTags}

	BrowserRequests = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &browserTags}
	MobileRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &mobileTags}
	ServerRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &serverTags}
)

type Measure struct {
	measures []*stats.Int64Measure
	tags     *[]tag.Mutator
}

func WithGauge(ctx context.Context, userAgent string, f func(), measure Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.GetGlobalContextLoggers(ctx).Errorf(`Failed to create tags: %s`, err)
	} else {
		for _, m := range measure.measures {
			ctx, _ := tag.New(ctx, *measure.tags...)
			stats.Record(ctx, m.M(1))
			defer stats.Record(ctx, m.M(-1))
		}
	}
	f()
}

func WithCount(ctx context.Context, userAgent string, f func(), measure Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.GetGlobalContextLoggers(ctx).Errorf(`Failed to create tag for user agent : %s`, err)
	} else {
		for _, m := range measure.measures {
			ctx, _ := tag.New(ctx, *measure.tags...)
			stats.Record(ctx, m.M(1))
		}
	}
	f()
}

// WithRouteCount Records a route hit and starts a trace. For stream connections, the duration of the stream connection is recorded
func WithRouteCount(ctx context.Context, userAgent, route, method string, f func(), measure Measure) {
	tagCtx, err := tag.New(ctx, tag.Insert(routeTagKey, sanitizeTagValue(route)), tag.Insert(methodTagKey, sanitizeTagValue(method)))
	if err != nil {
		logging.GetGlobalContextLoggers(ctx).Errorf(`Failed to create tags for route "%s %s": %s`, method, route, err)
	} else {
		ctx = tagCtx
	}
	ctx, span := trace.StartSpan(ctx, route)
	defer span.End()

	WithCount(ctx, userAgent, f, measure)
}
