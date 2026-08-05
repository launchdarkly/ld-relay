package metrics

import (
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	// Metric instrument names.
	connMeasureName             = "http.server.active_requests"
	requestDurationMeasureName  = "http.server.request.duration"
	eventsReceivedMeasureName   = "launchdarkly.relay.events.received.size"
	eventsSentMeasureName       = "launchdarkly.relay.events.sent"
	eventsSentSizeMeasureName   = "launchdarkly.relay.events.sent.size"
	eventsSendErrorsMeasureName = "launchdarkly.relay.events.send.errors"
	eventsDroppedMeasureName    = "launchdarkly.relay.events.dropped"
	eventsPendingMeasureName    = "launchdarkly.relay.events.pending"

	// Instrument names for the eventsource server SSE streams. These are recorded by the
	// otelbridge package from eventsource ServerTrace callbacks.
	streamSubscribersActiveMeasureName   = "launchdarkly.relay.stream.subscribers.active"
	streamConnectionDurationMeasureName  = "launchdarkly.relay.stream.connection.duration"
	streamSubscribersDroppedMeasureName  = "launchdarkly.relay.stream.subscribers.dropped"
	streamEventsSentMeasureName          = "launchdarkly.relay.stream.events.sent"
	streamEventsSentSizeMeasureName      = "launchdarkly.relay.stream.events.sent.size"
	streamCommentsSentMeasureName        = "launchdarkly.relay.stream.comments.sent"
	streamEventsDiscardedMeasureName     = "launchdarkly.relay.stream.events.discarded"
	streamWriteErrorsMeasureName         = "launchdarkly.relay.stream.write.errors"
	streamReplayEventsMeasureName        = "launchdarkly.relay.stream.replay.events"
	streamReplayDataSizeMeasureName      = "launchdarkly.relay.stream.replay.data.size"
	streamReplayDrainDurationMeasureName = "launchdarkly.relay.stream.replay.drain.duration"

	defaultFlushInterval = time.Minute

	BrowserPlatformCategory = "browser"
	MobilePlatformCategory  = "mobile"
	ServerPlatformCategory  = "server"
)

var (
	relayIDAttrKey            = attribute.Key("relay.id")            //nolint:gochecknoglobals
	platformCategoryAttrKey   = attribute.Key("platform.category")   //nolint:gochecknoglobals
	userAgentAttrKey          = attribute.Key("user_agent")          //nolint:gochecknoglobals
	sdkWrapperAttrKey         = attribute.Key("sdk.wrapper")         //nolint:gochecknoglobals
	envNameAttrKey            = attribute.Key("environment.name")    //nolint:gochecknoglobals
	applicationIDAttrKey      = attribute.Key("application.id")      //nolint:gochecknoglobals
	applicationVersionAttrKey = attribute.Key("application.version") //nolint:gochecknoglobals
	instanceIDAttrKey         = attribute.Key("instance.id")         //nolint:gochecknoglobals

	// OTEL HTTP semantic convention attribute keys (from semconv package)
	httpRouteAttrKey           = semconv.HTTPRouteKey              //nolint:gochecknoglobals
	httpRequestMethodAttrKey   = semconv.HTTPRequestMethodKey      //nolint:gochecknoglobals
	httpResponseStatusAttrKey  = semconv.HTTPResponseStatusCodeKey //nolint:gochecknoglobals
	urlSchemeAttrKey           = semconv.URLSchemeKey              //nolint:gochecknoglobals
	networkProtoVersionAttrKey = semconv.NetworkProtocolVersionKey //nolint:gochecknoglobals
	errorTypeAttrKey           = semconv.ErrorTypeKey              //nolint:gochecknoglobals
	statusCodeAttrKey          = attribute.Key("status_code")      //nolint:gochecknoglobals
)

// buildRequestAttributes creates an OTel attribute set for request metrics using semconv attribute names
// where applicable. All string values should be pre-sanitized via sanitizeTagValue before calling this function.
func buildRequestAttributes(baseKVs []attribute.KeyValue, platform, userAgent, sdkWrapper, route, method, urlScheme, applicationID, applicationVersion, instanceID string) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+9)
	copy(attrs, baseKVs)
	attrs = append(attrs,
		platformCategoryAttrKey.String(platform),
		userAgentAttrKey.String(userAgent),
		sdkWrapperAttrKey.String(sdkWrapper),
		httpRouteAttrKey.String(route),
		httpRequestMethodAttrKey.String(method),
		urlSchemeAttrKey.String(urlScheme),
		applicationIDAttrKey.String(applicationID),
		applicationVersionAttrKey.String(applicationVersion),
		instanceIDAttrKey.String(instanceID),
	)
	return attribute.NewSet(attrs...)
}

// buildDurationAttributes creates an OTel attribute set for the http.server.request.duration histogram,
// including all semconv required/conditionally-required attributes.
func buildDurationAttributes(baseKVs []attribute.KeyValue, platform, userAgent, sdkWrapper, route, method, applicationID, applicationVersion, instanceID, urlScheme, protocolVersion, errorType string, statusCode int) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+14)
	copy(attrs, baseKVs)
	attrs = append(attrs,
		platformCategoryAttrKey.String(platform),
		userAgentAttrKey.String(userAgent),
		sdkWrapperAttrKey.String(sdkWrapper),
		httpRouteAttrKey.String(route),
		httpRequestMethodAttrKey.String(method),
		urlSchemeAttrKey.String(urlScheme),
		applicationIDAttrKey.String(applicationID),
		applicationVersionAttrKey.String(applicationVersion),
		instanceIDAttrKey.String(instanceID),
	)
	if protocolVersion != "" {
		attrs = append(attrs, networkProtoVersionAttrKey.String(protocolVersion))
	}
	if statusCode > 0 {
		attrs = append(attrs, httpResponseStatusAttrKey.Int(statusCode))
	}
	if errorType != "" {
		attrs = append(attrs, errorTypeAttrKey.String(errorType))
	}
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
