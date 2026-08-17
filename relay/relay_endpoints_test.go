package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/middleware"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testenv"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v3/jsonhelpers"

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

func TestReportFlagEvalFailsWithUninitializedClientAndStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, st.MakeStoreWithData(false))
	req := buildPreRoutedRequest("REPORT", []byte(`{"key": "my-user"}`), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(basictypes.JSClientSDK, ct.OptBase2Bytes{})(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := io.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalRejectsOversizedBodyWhenLimitConfigured(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, st.MakeStoreWithData(true))

	maxBodySize, _ := ct.NewOptBase2BytesFromString("1KiB")
	oversized := make([]byte, 1024*2)
	for i := range oversized {
		oversized[i] = 'a'
	}
	req := buildPreRoutedRequest("REPORT", oversized, headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(basictypes.JSClientSDK, maxBodySize)(resp, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.Code)
}

// An unset body limit (ct.OptBase2Bytes{}) is not enforced: the test below issues the same REPORT with
// no limit configured and gets a 200, so it covers that branch as well as the body it asserts on.
func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	ctx := testenv.NewTestEnvContext("", false, st.MakeStoreWithData(true))
	req := buildPreRoutedRequest("REPORT", jsonhelpers.ToJSON(st.BasicUserForTestFlags), headers, nil, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(basictypes.JSClientSDK, ct.OptBase2Bytes{})(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := io.ReadAll(resp.Body)
	assert.JSONEq(t, st.MakeEvalBody(st.ClientSideFlags, false), string(b))
}
