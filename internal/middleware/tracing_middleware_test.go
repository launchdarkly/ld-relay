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
	router.Use(RedactContextFromSpanPath)
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

func TestContextIsRedactedFromSpanPath(t *testing.T) {
	for _, template := range contextRouteTemplates {
		t.Run(template, func(t *testing.T) {
			router, recorder := tracedRouter(t, template)
			// Substituting the context variable last leaves any other variable in place, so
			// the request path differs from the template only by the redacted value.
			path := strings.Replace(template, "{envId}", "507f1f77bcf86cd799439011", 1)
			path = strings.Replace(path, "{context}", contextBase64, 1)

			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))

			assert.Equal(t, template, requireURLPath(t, recorder))
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
	router.Use(RedactContextFromSpanPath)
	sdkRouter := router.PathPrefix("/sdk/").Subrouter()
	pollRouter := sdkRouter.PathPrefix("/poll/eval").Subrouter()
	pollRouter.HandleFunc("/{context}", func(http.ResponseWriter, *http.Request) {}).Methods("GET")

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sdk/poll/eval/"+contextBase64, nil))

	assert.Equal(t, "/sdk/poll/eval/{context}", requireURLPath(t, recorder))
}

// TestRedactionWithoutARecordingSpan covers a relay with tracing disabled, where the request
// span is a noop and there is nothing to overwrite.
func TestRedactionWithoutARecordingSpan(t *testing.T) {
	handled := false
	router := mux.NewRouter()
	router.Use(RedactContextFromSpanPath)
	router.HandleFunc("/meval/{context}", func(http.ResponseWriter, *http.Request) { handled = true }).Methods("GET")

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/meval/"+contextBase64, nil))

	assert.True(t, handled)
}
