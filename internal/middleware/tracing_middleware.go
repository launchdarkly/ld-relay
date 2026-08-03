package middleware

import (
	"net/http"

	"github.com/gorilla/mux"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// contextPathVar is the route variable that holds a base64-encoded evaluation context. It
// must match the variable name used by the routes in relay_routes.go.
const contextPathVar = "context"

// RedactContextFromSpanPath replaces the url.path attribute of the current request span
// with the matched route template, for routes that take an evaluation context in the path.
//
// The HTTP tracing middleware records the raw request path, which on those routes holds the
// end user's context: keys, names, emails and custom attributes. Attributes cannot be
// removed from a span once set, but they are deduplicated last-write-wins when they are
// read, so overwriting the value here keeps the context out of the exported trace. Nothing
// is lost by doing so: the same middleware records the route template as http.route.
//
// Routes with no context variable keep their real path.
//
// This must be registered after the middleware that starts the request span.
func RedactContextFromSpanPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, hasContext := mux.Vars(r)[contextPathVar]; hasContext {
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				// A route that produced a path variable always has a path template.
				template, _ := mux.CurrentRoute(r).GetPathTemplate()
				span.SetAttributes(semconv.URLPath(template))
			}
		}
		next.ServeHTTP(w, r)
	})
}
