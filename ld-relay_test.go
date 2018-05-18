package main

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"log"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	ld "gopkg.in/launchdarkly/go-client.v4"
)

type FakeLDClient struct{ initialized bool }

func (c FakeLDClient) Initialized() bool {
	return c.initialized
}

var nullLogger = log.New(ioutil.Discard, "", 0)
var emptyStore = ld.NewInMemoryFeatureStore(nullLogger)

// Returns a key matching the UUID header pattern
func key() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func handler() ClientMux {
	clients := map[string]clientContext{key(): &clientContextImpl{client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	return ClientMux{clientContextByKey: clients}
}

func clientSideHandler(allowedOrigins []string) ClientSideMux {
	testClientSideContext := &ClientSideContext{allowedOrigins: allowedOrigins, clientContext: &clientContextImpl{client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	contexts := map[string]*ClientSideContext{key(): testClientSideContext}
	return ClientSideMux{contextByKey: contexts}
}

func buildRequest(verb string, vars map[string]string, headers map[string]string, body string, ctx interface{}) *http.Request {
	req, _ := http.NewRequest(verb, "", bytes.NewBufferString(body))
	req = mux.SetURLVars(req, vars)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
	return req
}

func makeStoreWithData(initialized bool) ld.FeatureStore {
	zero := 0
	store := ld.NewInMemoryFeatureStore(nullLogger)
	if initialized {
		store.Init(nil)
	}
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "another-flag-key", On: true, Fallthrough: ld.VariationOrRollout{Variation: &zero}, Variations: []interface{}{3}, Version: 1})
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "some-flag-key", OffVariation: &zero, Variations: []interface{}{true}, Version: 2})
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "off-variation-key", Version: 3})
	return store
}

func makeTestContextWithData() *clientContextImpl {
	return &clientContextImpl{
		client: FakeLDClient{initialized: true},
		store:  makeStoreWithData(true),
		logger: nullLogger,
	}
}

func TestGetFlagEvalValueOnlySucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestReportFlagEvalValueOnlySucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{
"another-flag-key":{"value": 3, "variation": 0, "version" :1, "trackEvents": false},
"some-flag-key":{"value": true, "variation": 0, "version": 2, "trackEvents": false},
"off-variation-key":{"value": null, "version": 3, "trackEvents": false}
}`, string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{
"another-flag-key":{"value": 3, "variation": 0, "version" :1, "trackEvents": false},
"some-flag-key":{"value": true, "variation": 0, "version": 2, "trackEvents": false},
"off-variation-key":{"value": null, "version": 3, "trackEvents": false}
}`, string(b))
}

func TestAuthorizeMethodFailsOnInvalidAuthKey(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee", "Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}`, nil)
	resp := httptest.NewRecorder()
	handler().selectClientByAuthorizationKey(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}notjson`, nil)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestReportFlagEvalFailsWithMissingUserKey(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, "{}", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"User must have a 'key' attribute"}`, string(b))
}

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := &clientContextImpl{
		client: FakeLDClient{initialized: false},
		store:  makeStoreWithData(false),
		logger: nullLogger,
	}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := &clientContextImpl{
		client: FakeLDClient{initialized: false},
		store:  makeStoreWithData(true),
		logger: nullLogger,
	}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestFindEnvironmentFailsOnInvalidEnvId(t *testing.T) {
	vars := map[string]string{"envId": "blah", "user": user()}
	req := buildRequest("GET", vars, nil, "", nil)
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).selectClientByUrlParam(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly)).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

func TestOptionsHandlerSetsAllowHeader(t *testing.T) {
	method := "GET"
	req := buildRequest(method, nil, nil, "", nil)
	resp := httptest.NewRecorder()
	allowMethodOptionsHandler(method).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, resp.Header().Get("Allow"), method)
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildRequest("", nil, nil, "", nil)
	resp := httptest.NewRecorder()
	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "*")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Credentials"), "false")
		assert.Equal(t, w.Header().Get("Access-Control-Max-Age"), "300")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Methods"), "GET, REPORT")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type, Content-Length")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectDefaultHeadersWhenRequestHasOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForSpecifiedDomain(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForInvalidOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)

	handler().selectClientByAuthorizationKey(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

}
