package middleware

import (
	"net/http"
	"strings"

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

// SanitizeRequestSpan removes end-user data from the request span.
//
// url.path holds the raw request path, which on routes that take an evaluation context contains the
// end user's data: keys, names, emails and custom attributes. Only that one segment is replaced, with
// the REDACTED placeholder the semantic conventions use for scrubbing a URL, so the rest of the path
// -- which identifies the endpoint and the environment, and is not sensitive -- survives. Routes with
// no context variable keep their real path.
//
// Attributes cannot be removed from a span once set, but they are deduplicated last-write-wins when
// they are read, so overwriting the value here is what keeps the original out of the exported trace.
//
// This middleware does not repair invalid UTF-8. tracing.NewUTF8Sanitizer does that for every span,
// which covers each attribute the instrumentation derives from request data without naming them. The
// redacted path is the one exception: it is written after that sanitizer runs, so it is sanitized here.
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
