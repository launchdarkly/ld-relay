package metrics

import (
	"time"

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
)
