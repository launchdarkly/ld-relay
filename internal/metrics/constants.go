package metrics

import (
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
)

const (
	defaultMetricsPrefix = "launchdarkly_relay"

	browserTagValue = "browser"
	mobileTagValue  = "mobile"
	serverTagValue  = "server"

	defaultFlushInterval = time.Minute
)

var (
	relayIdTagKey, _          = tag.NewKey("relayId")
	platformCategoryTagKey, _ = tag.NewKey("platformCategory")
	userAgentTagKey, _        = tag.NewKey("userAgent")
	routeTagKey, _            = tag.NewKey("route")
	methodTagKey, _           = tag.NewKey("method")
	envNameTagKey, _          = tag.NewKey("env")

	publicTags  = []tag.Key{platformCategoryTagKey, userAgentTagKey, envNameTagKey}
	privateTags = []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIdTagKey, envNameTagKey}

	// For internal event exporter
	privateConnMeasure    = stats.Int64("internal_connections", "current number of connections", stats.UnitDimensionless)
	privateNewConnMeasure = stats.Int64("internal_newconnections", "total number of connections", stats.UnitDimensionless)

	connMeasure    = stats.Int64("connections", "current number of connections", stats.UnitDimensionless)
	newConnMeasure = stats.Int64("newconnections", "total number of connections", stats.UnitDimensionless)
	requestMeasure = stats.Int64("requests", "Number of hits to a route", stats.UnitDimensionless)
)
