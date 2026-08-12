package middleware

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/gorilla/mux"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

// contextPathVar is the route variable that holds a base64-encoded evaluation context. It
// must match the variable name used by the routes in relay_routes.go.
const contextPathVar = "context"

// redactedValue replaces a path segment whose contents must not be recorded. The semantic
// conventions use this placeholder for the same purpose when scrubbing a URL.
const redactedValue = "REDACTED"

// SanitizeRequestSpan corrects two problems with the attributes the HTTP tracing middleware records on
// the request span. Attributes cannot be removed from a span once set, but they are deduplicated
// last-write-wins when they are read, so overwriting a value here is what keeps the original out of the
// exported trace.
//
// First, url.path holds the raw request path, which on routes that take an evaluation context contains
// the end user's data: keys, names, emails and custom attributes. Only that one segment is replaced,
// with the REDACTED placeholder the semantic conventions use for scrubbing a URL, so the rest of the
// path -- which identifies the endpoint and the environment, and is not sensitive -- survives. Routes
// with no context variable keep their real path.
//
// Second, three of the recorded attributes come from request data that is not restricted to valid
// UTF-8, and OTLP cannot serialize a span carrying an invalid byte -- the marshal fails and the whole
// export batch is dropped. They are:
//
//	user_agent.original  the User-Agent header
//	client.address       the X-Forwarded-For header, used unvalidated
//	url.path             the percent-decoded request path
//
// This list is specific to the instrumentation in use and will need revisiting if it starts recording
// further attributes from request data; TestSpanAttributesAreValidUTF8 fails if that happens.
//
// This must be registered after the middleware that starts the request span.
func SanitizeRequestSpan(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
			if contextValue, hasContext := mux.Vars(r)[contextPathVar]; hasContext {
				// A route that produced a path variable always has a path template.
				template, _ := mux.CurrentRoute(r).GetPathTemplate()
				redacted := redactContextSegment(r.URL.Path, contextValue, template)
				span.SetAttributes(semconv.URLPath(util.SanitizeUTF8(redacted)))
			} else if !utf8.ValidString(r.URL.Path) {
				span.SetAttributes(semconv.URLPath(util.SanitizeUTF8(r.URL.Path)))
			}
			if ua := r.UserAgent(); !utf8.ValidString(ua) {
				span.SetAttributes(semconv.UserAgentOriginal(util.SanitizeUTF8(ua)))
			}
			if xff := r.Header.Get("X-Forwarded-For"); !utf8.ValidString(xff) {
				span.SetAttributes(semconv.ClientAddress(util.SanitizeUTF8(clientAddressFromXFF(xff))))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// redactContextSegment returns path with the whole segment holding the evaluation context replaced by
// the REDACTED placeholder. The comparison is per segment rather than a substring replacement, so a
// context value that also appears inside another segment cannot corrupt it.
//
// If no segment matches the context value, the route template is returned instead. That happens when
// the value does not line up with a single segment, as a context carrying an encoded slash would, and
// falling back keeps the invariant that end-user data never reaches a span: the template is the same
// value this attribute reported before the redaction became segment-scoped.
func redactContextSegment(path, contextValue, template string) string {
	segments := strings.Split(path, "/")
	found := false
	for i, segment := range segments {
		if segment == contextValue {
			segments[i] = redactedValue
			found = true
		}
	}
	if !found {
		return template
	}
	return strings.Join(segments, "/")
}

// clientAddressFromXFF mirrors how the tracing instrumentation derives client.address from
// X-Forwarded-For: the first entry, with no validation of its contents.
func clientAddressFromXFF(xForwardedFor string) string {
	for i := range len(xForwardedFor) {
		if xForwardedFor[i] == ',' {
			return xForwardedFor[:i]
		}
	}
	return xForwardedFor
}
