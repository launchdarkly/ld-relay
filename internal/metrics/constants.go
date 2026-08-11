package metrics

import (
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/util"

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

	defaultFlushInterval = time.Minute

	// notProvidedValue is the sentinel reported for an attribute whose value is absent.
	notProvidedValue = "not-provided"

	BrowserPlatformCategory = "browser"
	MobilePlatformCategory  = "mobile"
	ServerPlatformCategory  = "server"
)

// EndpointType identifies the kind of Relay endpoint that served a request. It is reported on
// request-scoped metrics as the relay.endpoint.type attribute, so that a metric covering all
// requests can still be broken down by the delivery mode the request belongs to.
type EndpointType string

const (
	// EndpointTypeStream is an SSE endpoint that holds the connection open.
	EndpointTypeStream EndpointType = "stream"

	// EndpointTypePoll is a request/response flag delivery endpoint.
	EndpointTypePoll EndpointType = "poll"

	// EndpointTypeEvents is an analytics or diagnostic event ingestion endpoint.
	EndpointTypeEvents EndpointType = "events"

	// EndpointTypeStatus is a Relay status endpoint. These are not associated with an SDK or an
	// LD environment.
	EndpointTypeStatus EndpointType = "status"

	// EndpointTypeGoals is the experimentation goals endpoint used by the JS SDK.
	EndpointTypeGoals EndpointType = "goals"

	// EndpointTypeNotProvided is used for requests that did not match any route Relay serves.
	EndpointTypeNotProvided EndpointType = notProvidedValue
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
	endpointTypeAttrKey       = attribute.Key("relay.endpoint.type") //nolint:gochecknoglobals

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
// where applicable. Values from the RequestInfo are sanitized here, so callers pass it through as-is.
func buildRequestAttributes(baseKVs []attribute.KeyValue, platform string, ri RequestInfo) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+10)
	copy(attrs, baseKVs)
	attrs = append(attrs, requestKVs(platform, ri)...)
	return attribute.NewSet(attrs...)
}

// buildDurationAttributes creates an OTel attribute set for the http.server.request.duration histogram,
// including all semconv required/conditionally-required attributes.
func buildDurationAttributes(baseKVs []attribute.KeyValue, platform string, ri RequestInfo) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+13)
	copy(attrs, baseKVs)
	attrs = append(attrs, requestKVs(platform, ri)...)
	if ri.ProtocolVersion != "" {
		attrs = append(attrs, networkProtoVersionAttrKey.String(ri.ProtocolVersion))
	}
	if ri.StatusCode > 0 {
		attrs = append(attrs, httpResponseStatusAttrKey.Int(ri.StatusCode))
	}
	if ri.ErrorType != "" {
		attrs = append(attrs, errorTypeAttrKey.String(ri.ErrorType))
	}
	return attribute.NewSet(attrs...)
}

// requestKVs returns the attributes that every request-scoped metric carries.
func requestKVs(platform string, ri RequestInfo) []attribute.KeyValue {
	return []attribute.KeyValue{
		platformCategoryAttrKey.String(sanitizeTagValue(platform)),
		userAgentAttrKey.String(sanitizeTagValue(ri.UserAgent)),
		sdkWrapperAttrKey.String(sanitizeTagValue(ri.SDKWrapper)),
		httpRouteAttrKey.String(sanitizeRouteValue(ri.Route)),
		httpRequestMethodAttrKey.String(sanitizeTagValue(ri.Method)),
		urlSchemeAttrKey.String(ri.URLScheme),
		applicationIDAttrKey.String(sanitizeTagValue(ri.ApplicationID)),
		applicationVersionAttrKey.String(sanitizeTagValue(ri.ApplicationVersion)),
		instanceIDAttrKey.String(sanitizeTagValue(ri.InstanceID)),
		endpointTypeAttrKey.String(sanitizeTagValue(string(ri.EndpointType))),
	}
}

// sanitizeTagValue ensures attribute values are valid.
// Empty values are replaced with descriptive defaults, and slashes are replaced with underscores.
// This is appropriate for user agent strings and SDK wrapper names, but not for routes.
//
// Invalid UTF-8 is stripped via util.SanitizeUTF8, which matters more than it looks: a single bad byte
// fails the OTLP marshal for the entire export batch, and these series are cumulative, so exports keep
// failing until the process restarts. Header values are not restricted to ASCII, so an unauthenticated
// caller can supply one via the status and not-found handlers.
func sanitizeTagValue(v string) string {
	v = util.SanitizeUTF8(strings.ReplaceAll(v, "/", "_"))
	if strings.TrimSpace(v) == "" {
		return notProvidedValue
	}
	return v
}

// sanitizeRouteValue ensures route attribute values are valid.
// Empty values are replaced with descriptive defaults. Unlike sanitizeTagValue,
// slashes are preserved since they are meaningful in route paths.
func sanitizeRouteValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return notProvidedValue
	}
	return v
}
