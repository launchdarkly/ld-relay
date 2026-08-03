package relay

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndUserContextIsNotExportedInSpans drives the polling routes that take an evaluation
// context in the path through a real relay, and asserts that no span produced while handling
// the request carries the context anywhere in its attributes. The HTTP tracing middleware
// records the raw request path, so without middleware.RedactContextFromSpanPath the base64
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
