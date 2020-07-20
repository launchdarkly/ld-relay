package relay

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	ct "github.com/launchdarkly/go-configtypes"
	c "github.com/launchdarkly/ld-relay/v6/config"
)

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := newTestEnvContext("", false, makeStoreWithData(false))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := newTestEnvContext("", false, makeStoreWithData(true))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, makeEvalBody(clientSideFlags, false, false), string(b))
}

func TestGetUserAgent(t *testing.T) {
	t.Run("X-LaunchDarkly-User-Agent takes precedence", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set(ldUserAgentHeader, "my-agent")
		req.Header.Set(userAgentHeader, "something-else")
		assert.Equal(t, "my-agent", getUserAgent(req))
	})
	t.Run("User-Agent is the fallback", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set(userAgentHeader, "my-agent")
		assert.Equal(t, "my-agent", getUserAgent(req))
	})
}

func TestRelayJSClientGoalsRoute(t *testing.T) {
	env := testEnvClientSide
	envID := env.config.EnvID
	fakeGoalsData := []byte(`["got some goals"]`)

	fakeGoalsEndpoint := mux.NewRouter()
	fakeGoalsEndpoint.HandleFunc("/sdk/goals/{envId}", func(w http.ResponseWriter, req *http.Request) {
		ioutil.ReadAll(req.Body)
		if mux.Vars(req)["envId"] != string(envID) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(fakeGoalsData)
	}).Methods("GET")
	fakeServerWithGoalsEndpoint := httptest.NewServer(fakeGoalsEndpoint)
	defer fakeServerWithGoalsEndpoint.Close()

	config := c.DefaultConfig
	config.Main.BaseURI, _ = ct.NewOptURLAbsoluteFromString(fakeServerWithGoalsEndpoint.URL)
	config.Environment = makeEnvConfigs(env)

	relayTest(config, func(p relayTestParams) {
		url := fmt.Sprintf("http://localhost/sdk/goals/%s", envID)

		t.Run("requests", func(t *testing.T) {
			r := buildRequest("GET", url, nil, nil)
			result, body := doRequest(r, p.relay)
			assertNonStreamingHeaders(t, result.Header)
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				assertExpectedCORSHeaders(t, result, "GET", "*")
			}
			expectBody(string(fakeGoalsData))(t, body)
		})

		t.Run("options", func(t *testing.T) {
			assertEndpointSupportsOptionsRequest(t, p.relay, url, "GET")
		})
	})
}
