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

	connMeasureName        = "connections"
	privateConnMeasureName = "internal_connections"

	newConnMeasureName        = "newconnections"
	privateNewConnMeasureName = "internal_newconnections"

	requestMeasureName = "requests"

	defaultFlushInterval = time.Minute
)

var (
	relayIDTagKey, _          = tag.NewKey("relayId")          //nolint:gochecknoglobals
	platformCategoryTagKey, _ = tag.NewKey("platformCategory") //nolint:gochecknoglobals
	userAgentTagKey, _        = tag.NewKey("userAgent")        //nolint:gochecknoglobals
	routeTagKey, _            = tag.NewKey("route")            //nolint:gochecknoglobals
	methodTagKey, _           = tag.NewKey("method")           //nolint:gochecknoglobals
	envNameTagKey, _          = tag.NewKey("env")              //nolint:gochecknoglobals

	publicTags  = []tag.Key{platformCategoryTagKey, userAgentTagKey, envNameTagKey}                //nolint:gochecknoglobals
	privateTags = []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIDTagKey, envNameTagKey} //nolint:gochecknoglobals
)
