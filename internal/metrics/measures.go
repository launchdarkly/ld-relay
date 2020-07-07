package metrics

import (
	"context"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	"github.com/launchdarkly/ld-relay/v6/internal/logging"
)

var (
	// These measures are kept in global variables, instead of being constructed wherever they are used,
	// because OpenCensus seems to track measures based on the actual measure instance used, rather than
	// by the name. At least, that's one theory; the documentation is unclear on this, but it does seem
	// that calling stats.Int64 with identical parameters in different parts of the code instead of reusing
	// an instance causes things to stop working.
	connMeasure    = stats.Int64(connMeasureName, "current number of connections", stats.UnitDimensionless)
	newConnMeasure = stats.Int64(newConnMeasureName, "total number of connections", stats.UnitDimensionless)
	requestMeasure = stats.Int64(requestMeasureName, "Number of hits to a route", stats.UnitDimensionless)

	// For internal event exporter
	privateConnMeasure    = stats.Int64(privateConnMeasureName, "current number of connections", stats.UnitDimensionless)
	privateNewConnMeasure = stats.Int64(privateNewConnMeasureName, "total number of connections", stats.UnitDimensionless)

	BrowserConns = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: makeBrowserTags()}
	MobileConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: makeMobileTags()}
	ServerConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: makeServerTags()}

	NewBrowserConns = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: makeBrowserTags()}
	NewMobileConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: makeMobileTags()}
	NewServerConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: makeServerTags()}

	BrowserRequests = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: makeBrowserTags()}
	MobileRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: makeMobileTags()}
	ServerRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: makeServerTags()}
)

type Measure struct {
	measures []*stats.Int64Measure
	tags     []tag.Mutator
}

func makeBrowserTags() []tag.Mutator {
	return []tag.Mutator{tag.Insert(platformCategoryTagKey, browserTagValue)}
}

func makeMobileTags() []tag.Mutator {
	return []tag.Mutator{tag.Insert(platformCategoryTagKey, mobileTagValue)}
}

func makeServerTags() []tag.Mutator {
	return []tag.Mutator{tag.Insert(platformCategoryTagKey, serverTagValue)}
}

func WithGauge(ctx context.Context, userAgent string, f func(), measure Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.GetGlobalContextLoggers(ctx).Errorf(`Failed to create tags: %s`, err)
	} else {
		for _, m := range measure.measures {
			ctx, _ := tag.New(ctx, measure.tags...)
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
			ctx, _ := tag.New(ctx, measure.tags...)
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
