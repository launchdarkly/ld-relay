package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

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
func tracedRouter(t *testing.T, templates ...string) (*mux.Router, *spanCapture) {
	t.Helper()
	recorder, provider := sanitizingProvider(t)

	router := mux.NewRouter()
	router.Use(otelmux.Middleware("", otelmux.WithTracerProvider(provider)))
	router.Use(SanitizeRequestSpan)
	for _, template := range templates {
		router.HandleFunc(template, func(http.ResponseWriter, *http.Request) {}).Methods("GET")
	}
	return router, recorder
}

// spanCapture collects exported spans. These tests go through an exporter rather than a span processor,
// because that is where the UTF-8 sanitizer runs in the pipeline tracing.NewTracingProvider builds.
type spanCapture struct {
	spans []sdktrace.ReadOnlySpan
}

func (c *spanCapture) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.spans = append(c.spans, spans...)
	return nil
}

func (c *spanCapture) Shutdown(context.Context) error { return nil }

// Ended matches the SpanRecorder method these tests were written against.
func (c *spanCapture) Ended() []sdktrace.ReadOnlySpan { return c.spans }

// sanitizingProvider builds the pipeline relay builds: a UTF-8 sanitizer wrapping the exporter.
// WithSyncer exports on span end, so a captured span is ready as soon as the request returns.
func sanitizingProvider(t *testing.T) (*spanCapture, *sdktrace.TracerProvider) {
	t.Helper()
	capture := &spanCapture{}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(tracing.NewUTF8SanitizingExporter(capture)),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return capture, provider
}

func requireSpanAttr(t *testing.T, recorder *spanCapture, key string) string {
	t.Helper()
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	for _, kv := range spans[0].Attributes() {
		if kv.Key == attribute.Key(key) {
			return kv.Value.AsString()
		}
	}
	require.Failf(t, "attribute is missing", "span has no %s attribute", key)
	return ""
}

func requireURLPath(t *testing.T, recorder *spanCapture) string {
	t.Helper()
	return requireSpanAttr(t, recorder, "url.path")
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
	recorder, provider := sanitizingProvider(t)

	router := mux.NewRouter()
	router.Use(otelmux.Middleware("", otelmux.WithTracerProvider(provider)))
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

// The Host is not restricted to valid UTF-8. The HTTP/1.1 server rejects a Host that holds a byte
// outside US-ASCII, but the HTTP/2 server passes the :authority pseudo-header through as it arrives,
// and Relay serves HTTP/2 whenever TLS is configured. One invalid byte fails the OTLP marshal for
// every span in the same export batch.
func TestInvalidUTF8IsStrippedFromTheServerAddress(t *testing.T) {
	for _, params := range []struct{ name, host, want string }{
		{"host and port", "relay-\xff\xfe.example.com:8030", "relay-.example.com"},
		{"host only", "relay-\xff\xfe.example.com", "relay-.example.com"},
		{"ipv6 literal", "[fe80::\xff\xfe]:8030", "fe80::"},
		{"nothing left", "\xff\xfe", ""},
	} {
		t.Run(params.name, func(t *testing.T) {
			router, recorder := tracedRouter(t, "/status")
			req := httptest.NewRequest("GET", "/status", nil)
			req.Host = params.host

			router.ServeHTTP(httptest.NewRecorder(), req)

			recorded := requireSpanAttr(t, recorder, "server.address")
			assert.Equal(t, params.want, recorded)
			assert.True(t, utf8.ValidString(recorded))
		})
	}
}

// A Host that is already valid UTF-8 is left to the instrumentation, port and all.
func TestAValidHostIsNotRewritten(t *testing.T) {
	router, recorder := tracedRouter(t, "/status")
	req := httptest.NewRequest("GET", "/status", nil)
	req.Host = "relay.example.com:8030"

	router.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "relay.example.com", requireSpanAttr(t, recorder, "server.address"))
}

// A Host the instrumentation cannot split reports an empty address, and the sanitizer has to leave
// that alone rather than substitute something of its own.
func TestAnUnparsableHostReportsNoServerAddress(t *testing.T) {
	for _, host := range []string{"[fe80::1", "relay.example.com:80:90"} {
		t.Run(host, func(t *testing.T) {
			router, recorder := tracedRouter(t, "/status")
			req := httptest.NewRequest("GET", "/status", nil)
			req.Host = host

			router.ServeHTTP(httptest.NewRecorder(), req)

			assert.Empty(t, requireSpanAttr(t, recorder, "server.address"))
		})
	}
}

// The instrumentation reads more request fields than the Host, and the ones below are outside the
// standalone server's control but not outside a host application's: relay can be embedded behind
// middleware that rewrites RemoteAddr from a forwarded header. The sanitizer covers whatever the
// instrumentation recorded, so no field needs to be enumerated here.
func TestInvalidUTF8IsStrippedFromEveryRequestDerivedAttribute(t *testing.T) {
	for _, params := range []struct {
		name   string
		mutate func(*http.Request)
		attrs  map[string]string
	}{
		{
			name:   "peer address",
			mutate: func(r *http.Request) { r.RemoteAddr = "10.0.0.1\xff\xfe:4242" },
			// client.address falls back to the peer when X-Forwarded-For is absent.
			attrs: map[string]string{"network.peer.address": "10.0.0.1", "client.address": "10.0.0.1"},
		},
		{
			name:   "protocol version",
			mutate: func(r *http.Request) { r.Proto = "HTTP/1.\xff\xfe" },
			attrs:  map[string]string{"network.protocol.version": "1."},
		},
		{
			name:   "user agent",
			mutate: func(r *http.Request) { r.Header.Set("User-Agent", "agent-\xff\xfe") },
			attrs:  map[string]string{"user_agent.original": "agent-"},
		},
		{
			name:   "forwarded for",
			mutate: func(r *http.Request) { r.Header.Set("X-Forwarded-For", "1.2.3.4\xff\xfe, 5.6.7.8") },
			attrs:  map[string]string{"client.address": "1.2.3.4"},
		},
	} {
		t.Run(params.name, func(t *testing.T) {
			router, recorder := tracedRouter(t, "/status")
			req := httptest.NewRequest("GET", "/status", nil)
			params.mutate(req)

			router.ServeHTTP(httptest.NewRecorder(), req)

			for key, want := range params.attrs {
				recorded := requireSpanAttr(t, recorder, key)
				assert.Equal(t, want, recorded, "attribute %s", key)
				assert.True(t, utf8.ValidString(recorded), "attribute %s", key)
			}
		})
	}
}

// The span name also carries request data: the instrumentation interpolates the request method into
// it for a request that matches no route.
func TestInvalidUTF8IsStrippedFromTheSpanName(t *testing.T) {
	recorder, provider := sanitizingProvider(t)

	tracer := provider.Tracer("test")
	_, span := tracer.Start(context.Background(), "HTTP GET\xff\xfe route not found")
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "HTTP GET route not found", spans[0].Name())
}
