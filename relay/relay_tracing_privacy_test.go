package relay

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndUserContextIsNotExportedInSpans drives the polling routes that take an evaluation
// context in the path through a real relay, and asserts that no span produced while handling
// the request carries the context anywhere in its attributes. The HTTP tracing middleware
// records the raw request path, so without middleware.SanitizeRequestSpan the base64
// context - keys, names, emails, custom attributes - is exported to the tracing backend.
//
// The streaming eval routes carry a context in the path too; they are covered by the
// middleware's own tests, since exercising them here would mean holding connections open.
func TestEndUserContextIsNotExportedInSpans(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvClientSide, st.EnvWithAllCredentials)

	withStartedRelay(t, config, func(p relayTestParams) {
		contextJSON := `{"kind":"user","key":"user@example.com","name":"Jane Doe","email":"jane@example.com"}`
		contextBase64 := base64.StdEncoding.EncodeToString([]byte(contextJSON))

		sdkKey := st.EnvMain.Config.SDKKey
		mobileKey := st.EnvWithAllCredentials.Config.MobileKey
		envID := string(st.EnvClientSide.Config.EnvID)

		cases := []struct {
			route string
			req   *http.Request
		}{
			{
				route: "/sdk/evalx/contexts/{context}",
				req:   st.BuildRequestWithAuth("GET", "/sdk/evalx/contexts/"+contextBase64, sdkKey, nil),
			},
			{
				route: "/sdk/evalx/users/{context}",
				req:   st.BuildRequestWithAuth("GET", "/sdk/evalx/users/"+contextBase64, sdkKey, nil),
			},
			{
				route: "/sdk/evalx/{envId}/contexts/{context}",
				req:   st.BuildRequest("GET", "/sdk/evalx/"+envID+"/contexts/"+contextBase64, nil, nil),
			},
			{
				route: "/sdk/evalx/{envId}/users/{context}",
				req:   st.BuildRequest("GET", "/sdk/evalx/"+envID+"/users/"+contextBase64, nil, nil),
			},
			{
				route: "/sdk/poll/eval/{context}",
				req:   st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil),
			},
			{
				route: "/msdk/evalx/contexts/{context}",
				req:   st.BuildRequestWithAuth("GET", "/msdk/evalx/contexts/"+contextBase64, mobileKey, nil),
			},
			{
				route: "/msdk/evalx/users/{context}",
				req:   st.BuildRequestWithAuth("GET", "/msdk/evalx/users/"+contextBase64, mobileKey, nil),
			},
		}

		for _, params := range cases {
			t.Run(params.route, func(t *testing.T) {
				recorder.Reset()
				w := httptest.NewRecorder()

				p.relay.Handler.ServeHTTP(w, params.req)

				// A route that did not match, or a request that was rejected before reaching
				// the handler, would make the assertions below vacuous.
				require.Equal(t, http.StatusOK, w.Code)
				spans := recorder.Ended()
				require.NotEmpty(t, spans)

				for _, span := range spans {
					for _, kv := range span.Attributes() {
						assert.NotContains(t, kv.Value.String(), contextBase64,
							"span %q attribute %q exports the end-user context", span.Name(), kv.Key)
						assert.NotContains(t, kv.Value.String(), contextJSON,
							"span %q attribute %q exports the end-user context", span.Name(), kv.Key)
					}
				}

				// The path is reported as the route template, which is also what http.route
				// holds, so nothing is lost by redacting it.
				root := rootSpan(t, spans)
				attrs := spanAttrs(root)
				assert.Equal(t, params.route, attrs["url.path"].AsString())
				assert.Equal(t, params.route, attrs["http.route"].AsString())
				assert.NotContains(t, root.Name(), contextBase64)
			})
		}
	})
}

// TestSpanAttributesAreValidUTF8 drives hostile request data through the relay and asserts that no span
// field carries invalid UTF-8. OTLP cannot serialize such a span: the marshal fails and the whole export
// batch is dropped, so one bad request costs every span batched alongside it.
//
// This deliberately checks *every* string attribute rather than the three the tracing instrumentation
// is known to derive from request data (user_agent.original, client.address, url.path). If it starts
// recording another one, this fails and points at what SanitizeRequestSpan has to cover.
func TestSpanAttributesAreValidUTF8(t *testing.T) {
	const invalid = "\xff\xfe"

	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		poison := func(req *http.Request) *http.Request {
			req.Header.Set("User-Agent", "agent-"+invalid)
			req.Header.Set("X-Forwarded-For", "1.2.3.4"+invalid+", 5.6.7.8")
			return req
		}

		requests := []*http.Request{
			// Unauthenticated, and the path variable decodes to invalid bytes.
			poison(st.BuildRequest("GET", "/status/%ff%fe", nil, nil)),
			poison(st.BuildRequest("GET", "/status", nil, nil)),
			// Authenticated: exercises relay.store.key, which comes from a decoded path variable.
			poison(st.BuildRequestWithAuth("GET", "/sdk/flags/%ff%fe", st.EnvMain.Config.SDKKey, nil)),
			poison(st.BuildRequestWithAuth("GET", "/sdk/poll", st.EnvMain.Config.SDKKey, nil)),
		}
		for _, req := range requests {
			st.DoRequest(req, p.relay)
		}

		spans := recorder.Ended()
		require.NotEmpty(t, spans, "no spans were recorded")

		checked := 0
		for _, s := range spans {
			assert.True(t, utf8.ValidString(s.Name()), "span name is not valid UTF-8: %q", s.Name())
			for _, kv := range s.Attributes() {
				if kv.Value.Type() != attribute.STRING {
					continue
				}
				checked++
				assert.True(t, utf8.ValidString(kv.Value.AsString()),
					"span %q attribute %s is not valid UTF-8: %q -- this fails the whole OTLP export batch",
					s.Name(), kv.Key, kv.Value.AsString())
			}
		}
		assert.Positive(t, checked, "no string attributes were examined")
	})
}
