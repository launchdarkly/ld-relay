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
	"github.com/launchdarkly/ld-relay/v6/core/middleware"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testenv"
)

// Shortcut for building a request when we are going to be passing it directly to an endpoint handler, rather than
// going through the usual routing mechanism, so we must provide the Context and the URL path variables explicitly.
func buildPreRoutedRequest(verb string, body []byte, headers http.Header, vars map[string]string, ctx relayenv.EnvContext) *http.Request {
	req := sharedtest.BuildRequest(verb, "", body, headers)
	req = mux.SetURLVars(req, vars)
	req = req.WithContext(middleware.WithEnvContextInfo(req.Context(), middleware.EnvContextInfo{Env: ctx}))
	return req
}

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, sharedtest.MakeStoreWithData(false))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(sdks.JSClient)(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, sharedtest.MakeStoreWithData(true))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(sdks.JSClient)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, st.MakeEvalBody(st.ClientSideFlags, false, false), string(b))
}

func DoJSClientGoalsEndpointTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
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

	var config c.Config
	config.Main.BaseURI, _ = ct.NewOptURLAbsoluteFromString(fakeServerWithGoalsEndpoint.URL)
	config.Environment = st.MakeEnvConfigs(env)

	DoTest(config, constructor, func(p TestParams) {
		url := fmt.Sprintf("http://localhost/sdk/goals/%s", envID)

		t.Run("requests", func(t *testing.T) {
			r := st.BuildRequest("GET", url, nil, nil)
			result, body := st.DoRequest(r, p.Handler)
			st.AssertNonStreamingHeaders(t, result.Header)
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				st.AssertExpectedCORSHeaders(t, result, "GET", "*")
			}
			st.ExpectBody(string(fakeGoalsData))(t, body)
		})

		t.Run("options", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.Handler, url, "GET")
		})
	})
}
