package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextBase64 is a context blob of the kind the client-side SDKs put in the path: it holds
// personal data that must never reach the tracing backend.
var contextBase64 = base64.StdEncoding.EncodeToString(
	[]byte(`{"kind":"user","key":"user@example.com","name":"Jane Doe","email":"jane@example.com"}`),
)

// contextRouteTemplates is every relay route template that takes an evaluation context in the
// path. Keep in step with relay_routes.go.
var contextRouteTemplates = []string{
	"/sdk/evalx/{envId}/contexts/{context}",
	"/sdk/evalx/{envId}/users/{context}",
	"/sdk/evalx/contexts/{context}",
	"/sdk/evalx/users/{context}",
	"/sdk/poll/eval/{context}",
	"/sdk/stream/eval/{context}",
	"/msdk/evalx/contexts/{context}",
	"/msdk/evalx/users/{context}",
	"/meval/{context}",
	"/eval/{envId}/{context}",
}

// tracedRouter builds a router instrumented the same way as relay's, recording its spans, and
// registers the given templates with a handler that does nothing.
func tracedRouter(t *testing.T, templates ...string) (*mux.Router, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := mux.NewRouter()
	router.Use(otelmux.Middleware("test", otelmux.WithTracerProvider(provider)))
	router.Use(SanitizeRequestSpan)
	for _, template := range templates {
		router.HandleFunc(template, func(http.ResponseWriter, *http.Request) {}).Methods("GET")
	}
	return router, recorder
}

func requireURLPath(t *testing.T, recorder *tracetest.SpanRecorder) string {
	t.Helper()
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	for _, kv := range spans[0].Attributes() {
		if kv.Key == attribute.Key("url.path") {
			return kv.Value.AsString()
		}
	}
	require.Fail(t, "span has no url.path attribute")
	return ""
}

// Only the context segment is replaced. Everything else in the path, including the environment
// identifier, is not sensitive and stays, so url.path still says which endpoint and which environment
// served the request rather than collapsing to a copy of http.route.
func TestContextIsRedactedFromSpanPath(t *testing.T) {
	for _, template := range contextRouteTemplates {
		t.Run(template, func(t *testing.T) {
			router, recorder := tracedRouter(t, template)
			path := strings.Replace(template, "{envId}", "507f1f77bcf86cd799439011", 1)
			path = strings.Replace(path, "{context}", contextBase64, 1)

			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))

			want := strings.Replace(template, "{envId}", "507f1f77bcf86cd799439011", 1)
			want = strings.Replace(want, "{context}", "REDACTED", 1)
			recorded := requireURLPath(t, recorder)
			assert.Equal(t, want, recorded)
			assert.NotContains(t, recorded, contextBase64, "the context must never reach the span")
		})
	}
}

// A context value that does not line up with a whole path segment cannot be redacted segment-wise. The
// fallback reports the route template, so end-user data still never reaches the span.
func TestRedactContextSegmentFallsBackToTheTemplate(t *testing.T) {
	const template = "/sdk/evalx/{envId}/contexts/{context}"

	specs := []struct {
		name         string
		path         string
		contextValue string
		want         string
	}{
		{
			name:         "context is a whole segment",
			path:         "/sdk/evalx/env-1/contexts/" + contextBase64,
			contextValue: contextBase64,
			want:         "/sdk/evalx/env-1/contexts/REDACTED",
		},
		{
			name:         "context spans segments",
			path:         "/sdk/evalx/env-1/contexts/one/two",
			contextValue: "one/two",
			want:         template,
		},
		{
			name:         "context value not present in the path",
			path:         "/sdk/evalx/env-1/contexts/something-else",
			contextValue: contextBase64,
			want:         template,
		},
		{
			name:         "a value repeated in another segment is redacted too",
			path:         "/sdk/evalx/" + contextBase64 + "/contexts/" + contextBase64,
			contextValue: contextBase64,
			want:         "/sdk/evalx/REDACTED/contexts/REDACTED",
		},
	}

	for _, tt := range specs {
		t.Run(tt.name, func(t *testing.T) {
			got := redactContextSegment(tt.path, tt.contextValue, template)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, tt.contextValue, "the context must never survive redaction")
		})
	}
}

func TestPathsWithoutAContextAreLeftAlone(t *testing.T) {
	// Routes with no context variable: their variables are flag and segment keys, environment
	// identifiers and payload filter keys, all of which are useful in a trace.
	for _, params := range []struct{ template, path string }{
		{"/sdk/flags/{key}", "/sdk/flags/my-flag-key"},
		{"/sdk/segments/{key}", "/sdk/segments/my-segment-key"},
		{"/sdk/goals/{envId}", "/sdk/goals/507f1f77bcf86cd799439011"},
		{"/status/{projKey}/{envKey}/filters/{filterKey}", "/status/my-proj/my-env/filters/my-filter"},
		{"/status", "/status"},
	} {
		t.Run(params.template, func(t *testing.T) {
			router, recorder := tracedRouter(t, params.template)

			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", params.path, nil))

			assert.Equal(t, params.path, requireURLPath(t, recorder))
		})
	}
}

// TestRedactionSurvivesNestedSubrouters covers the way relay actually declares these routes:
// the context route lives several subrouters deep, and the middleware runs on the top-level
// router, so it must still see the full template of the innermost matched route.
func TestRedactionSurvivesNestedSubrouters(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := mux.NewRouter()
	router.Use(otelmux.Middleware("test", otelmux.WithTracerProvider(provider)))
	router.Use(SanitizeRequestSpan)
	sdkRouter := router.PathPrefix("/sdk/").Subrouter()
	pollRouter := sdkRouter.PathPrefix("/poll/eval").Subrouter()
	pollRouter.HandleFunc("/{context}", func(http.ResponseWriter, *http.Request) {}).Methods("GET")

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sdk/poll/eval/"+contextBase64, nil))

	assert.Equal(t, "/sdk/poll/eval/REDACTED", requireURLPath(t, recorder))
}

// TestRedactionWithoutARecordingSpan covers a relay with tracing disabled, where the request
// span is a noop and there is nothing to overwrite.
func TestRedactionWithoutARecordingSpan(t *testing.T) {
	handled := false
	router := mux.NewRouter()
	router.Use(SanitizeRequestSpan)
	router.HandleFunc("/meval/{context}", func(http.ResponseWriter, *http.Request) { handled = true }).Methods("GET")

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/meval/"+contextBase64, nil))

	assert.True(t, handled)
}
