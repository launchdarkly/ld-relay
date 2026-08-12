package metrics

import (
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
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

	// notProvidedValue is the sentinel reported for an OTel attribute whose value is absent. It is
	// snake_case to match the other attribute values Relay reports, such as the auth results on
	// spans.
	//
	// OTel's own convention is to omit an absent attribute rather than fill one in. Relay fills one
	// in deliberately: the increment and decrement of http.server.active_requests must carry
	// identical attribute sets or the series never returns to zero, and Prometheus handles a label
	// that is present on only some series of a metric poorly.
	notProvidedValue = "not_provided"

	// usageNotProvidedValue is the sentinel for an absent value in the usage data Relay reports to
	// LaunchDarkly. That payload is a wire contract with a different consumer, so it keeps its own
	// spelling rather than following the OTel attribute value above.
	usageNotProvidedValue = "not-provided"

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

// Attributes deliberately absent from these sets:
//
//   - relay.id is a resource attribute (see tracing.NewResource): it never varies within a process.
//   - instance.id, platform.category and sdk.wrapper were dropped. instance.id is per SDK instance, so
//     it made every request metric effectively per-client-process; the other two are largely implied by
//     http.route. All three are still reported in the usage data Relay sends to LaunchDarkly, which is
//     a separate sink with its own cardinality budget.
var (
	envNameAttrKey            = attribute.Key("environment.name")    //nolint:gochecknoglobals
	applicationIDAttrKey      = attribute.Key("application.id")      //nolint:gochecknoglobals
	applicationVersionAttrKey = attribute.Key("application.version") //nolint:gochecknoglobals
	endpointTypeAttrKey       = attribute.Key("relay.endpoint.type") //nolint:gochecknoglobals

	// OTEL HTTP semantic convention attribute keys (from semconv package)
	userAgentAttrKey           = semconv.UserAgentOriginalKey      //nolint:gochecknoglobals
	httpRouteAttrKey           = semconv.HTTPRouteKey              //nolint:gochecknoglobals
	httpRequestMethodAttrKey   = semconv.HTTPRequestMethodKey      //nolint:gochecknoglobals
	httpResponseStatusAttrKey  = semconv.HTTPResponseStatusCodeKey //nolint:gochecknoglobals
	urlSchemeAttrKey           = semconv.URLSchemeKey              //nolint:gochecknoglobals
	networkProtoVersionAttrKey = semconv.NetworkProtocolVersionKey //nolint:gochecknoglobals
	errorTypeAttrKey           = semconv.ErrorTypeKey              //nolint:gochecknoglobals
)

// errorTypeOther is the semantic-convention value for an error that the instrumentation cannot
// classify further.
const errorTypeOther = "_OTHER"

// buildRequestAttributes creates an OTel attribute set for request metrics using semconv attribute names
// where applicable. Values from the RequestInfo are sanitized here, so callers pass it through as-is.
func buildRequestAttributes(baseKVs []attribute.KeyValue, ri RequestInfo) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+7)
	copy(attrs, baseKVs)
	attrs = append(attrs, requestKVs(ri)...)
	return attribute.NewSet(attrs...)
}

// buildDurationAttributes creates an OTel attribute set for the http.server.request.duration histogram,
// including all semconv required/conditionally-required attributes.
func buildDurationAttributes(baseKVs []attribute.KeyValue, ri RequestInfo) attribute.Set {
	attrs := make([]attribute.KeyValue, len(baseKVs), len(baseKVs)+10)
	copy(attrs, baseKVs)
	attrs = append(attrs, requestKVs(ri)...)
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

// requestKVs returns the attributes that every request-scoped metric carries. Keeping this at seven,
// plus environment.name from the environment, holds the common case within attribute.NewSet's
// fixed-size fast path, which only covers sets of ten or fewer.
func requestKVs(ri RequestInfo) []attribute.KeyValue {
	return []attribute.KeyValue{
		userAgentAttrKey.String(sanitizeVerbatimValue(ri.UserAgent)),
		httpRouteAttrKey.String(sanitizeRouteValue(ri.Route)),
		httpRequestMethodAttrKey.String(sanitizeTagValue(ri.Method)),
		urlSchemeAttrKey.String(ri.URLScheme),
		applicationIDAttrKey.String(sanitizeTagValue(ri.ApplicationID)),
		applicationVersionAttrKey.String(sanitizeTagValue(ri.ApplicationVersion)),
		endpointTypeAttrKey.String(sanitizeTagValue(string(ri.EndpointType))),
	}
}

// sanitizeTagValue ensures OTel attribute values are valid.
// Empty values are replaced with the absent-value sentinel, and slashes are replaced with underscores.
// This is not appropriate for routes, where slashes are meaningful, nor for values that a semantic
// convention defines as verbatim.
func sanitizeTagValue(v string) string {
	return sanitizeReplacingSlashes(v, notProvidedValue)
}

// sanitizeUsageTagValue is sanitizeTagValue for the usage data Relay reports to LaunchDarkly. It
// exists only to keep that payload's absent-value sentinel independent of the OTel one: the two
// sinks have different consumers, so renaming an attribute value must not change what the usage
// events carry.
func sanitizeUsageTagValue(v string) string {
	return sanitizeReplacingSlashes(v, usageNotProvidedValue)
}

// sanitizeReplacingSlashes replaces slashes with underscores and substitutes absent for a value that
// is empty once sanitized.
//
// Invalid UTF-8 is stripped via util.SanitizeUTF8, which matters more than it looks: a single bad byte
// fails the OTLP marshal for the entire export batch, and these series are cumulative, so exports keep
// failing until the process restarts. Header values are not restricted to ASCII, so an unauthenticated
// caller can supply one via the status and not-found handlers.
func sanitizeReplacingSlashes(v, absent string) string {
	v = util.SanitizeUTF8(strings.ReplaceAll(v, "/", "_"))
	if strings.TrimSpace(v) == "" {
		return absent
	}
	return v
}

// sanitizeVerbatimValue keeps a value as the client sent it, stripping only invalid UTF-8. Use it for
// attributes whose semantic convention defines them as the original value, such as
// user_agent.original: replacing the slashes in "Node/3.4.0" would make the metric attribute disagree
// with the identical attribute the tracing instrumentation records on the request span, so the two
// could no longer be joined.
func sanitizeVerbatimValue(v string) string {
	v = util.SanitizeUTF8(v)
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
