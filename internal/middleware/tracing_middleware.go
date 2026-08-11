package middleware

import (
	"net/http"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/gorilla/mux"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// contextPathVar is the route variable that holds a base64-encoded evaluation context. It
// must match the variable name used by the routes in relay_routes.go.
const contextPathVar = "context"

// SanitizeRequestSpan corrects two problems with the attributes the HTTP tracing middleware records on
// the request span. Attributes cannot be removed from a span once set, but they are deduplicated
// last-write-wins when they are read, so overwriting a value here is what keeps the original out of the
// exported trace.
//
// First, url.path holds the raw request path, which on routes that take an evaluation context contains
// the end user's data: keys, names, emails and custom attributes. Those are replaced with the matched
// route template. Nothing is lost, since the same middleware also records the template as http.route.
// Routes with no context variable keep their real path.
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
			if _, hasContext := mux.Vars(r)[contextPathVar]; hasContext {
				// A route that produced a path variable always has a path template.
				template, _ := mux.CurrentRoute(r).GetPathTemplate()
				span.SetAttributes(semconv.URLPath(template))
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
