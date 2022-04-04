package core

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/middleware"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest/testenv"

	"github.com/launchdarkly/go-test-helpers/v2/jsonhelpers"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

// Shortcut for building a request when we are going to be passing it directly to an endpoint handler, rather than
// going through the usual routing mechanism, so we must provide the Context and the URL path variables explicitly.
func buildPreRoutedRequest(verb string, body []byte, headers http.Header, vars map[string]string, ctx relayenv.EnvContext) *http.Request {
	req := st.BuildRequest(verb, "", body, headers)
	req = mux.SetURLVars(req, vars)
	req = req.WithContext(middleware.WithEnvContextInfo(req.Context(), middleware.EnvContextInfo{
		Env: ctx,
	}))
	return req
}

func TestObscureKey(t *testing.T) {
	assert.Equal(t, "********-**-*89abc", ObscureKey("def01234-56-789abc"))
	assert.Equal(t, "sdk-********-**-*89abc", ObscureKey("sdk-def01234-56-789abc"))
	assert.Equal(t, "mob-********-**-*89abc", ObscureKey("mob-def01234-56-789abc"))
	assert.Equal(t, "89abc", ObscureKey("89abc"))
	assert.Equal(t, "9abc", ObscureKey("9abc"))
	assert.Equal(t, "sdk-9abc", ObscureKey("sdk-9abc"))
}

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, st.MakeStoreWithData(false))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(basictypes.JSClientSDK)(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, st.MakeStoreWithData(true))
	req := buildPreRoutedRequest("REPORT", jsonhelpers.ToJSON(st.BasicUserForTestFlags), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(basictypes.JSClientSDK)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, st.MakeEvalBody(st.ClientSideFlags, false, false), string(b))
}
