package metrics

import (
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

const (
	// Metric instrument names.
	connMeasureName                = "launchdarkly.relay.connections"
	requestMeasureName             = "launchdarkly.relay.requests"
	requestDurationMeasureName     = "launchdarkly.relay.request.duration"
	eventsIngestedBytesMeasureName = "launchdarkly.relay.events.ingested.bytes"

	defaultFlushInterval = time.Minute

	BrowserPlatformCategory = "browser"
	MobilePlatformCategory  = "mobile"
	ServerPlatformCategory  = "server"
)

var (
	relayIDAttrKey          = attribute.Key("relayId")          //nolint:gochecknoglobals
	platformCategoryAttrKey = attribute.Key("platformCategory") //nolint:gochecknoglobals
	userAgentAttrKey        = attribute.Key("userAgent")        //nolint:gochecknoglobals
	sdkWrapperAttrKey       = attribute.Key("sdkWrapper")       //nolint:gochecknoglobals
	routeAttrKey            = attribute.Key("route")            //nolint:gochecknoglobals
	methodAttrKey           = attribute.Key("method")           //nolint:gochecknoglobals
	envNameAttrKey          = attribute.Key("env")              //nolint:gochecknoglobals
)

// buildAttributes creates an OTel attribute set from base key-values plus per-request attributes.
// All string values should be pre-sanitized via sanitizeTagValue before calling this function.
func buildAttributes(baseKVs []attribute.KeyValue, platform, userAgent, sdkWrapper string) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+3)
	copy(attrs, baseKVs)
	attrs = append(attrs,
		platformCategoryAttrKey.String(platform),
		userAgentAttrKey.String(userAgent),
		sdkWrapperAttrKey.String(sdkWrapper),
	)
	return attribute.NewSet(attrs...)
}

// buildRequestAttributes creates an OTel attribute set for request metrics (includes route and method).
// All string values should be pre-sanitized via sanitizeTagValue before calling this function.
func buildRequestAttributes(baseKVs []attribute.KeyValue, platform, userAgent, sdkWrapper, route, method string) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+5)
	copy(attrs, baseKVs)
	attrs = append(attrs,
		platformCategoryAttrKey.String(platform),
		userAgentAttrKey.String(userAgent),
		sdkWrapperAttrKey.String(sdkWrapper),
		routeAttrKey.String(route),
		methodAttrKey.String(method),
	)
	return attribute.NewSet(attrs...)
}

// sanitizeTagValue ensures attribute values are valid.
// Empty values are replaced with descriptive defaults, and slashes are replaced with underscores.
// This is appropriate for user agent strings and SDK wrapper names, but not for routes.
func sanitizeTagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not-provided"
	}
	return strings.ReplaceAll(v, "/", "_")
}

// sanitizeRouteValue ensures route attribute values are valid.
// Empty values are replaced with descriptive defaults. Unlike sanitizeTagValue,
// slashes are preserved since they are meaningful in route paths.
func sanitizeRouteValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not-provided"
	}
	return v
}
