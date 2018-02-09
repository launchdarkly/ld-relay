package main

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	ld "gopkg.in/launchdarkly/go-client.v3"
)

type FakeLDClient struct {
	mock.Mock
}

func (client *FakeLDClient) AllFlags(user ld.User) map[string]interface{} {
	flags := make(map[string]interface{})
	flags["some-flag-key"] = true
	flags["another-flag-key"] = 3
	return flags
}

// Returns a key matching the UUID header pattern
func key() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func handler() ClientMux {
	clients := map[string]FlagReader{key(): &FakeLDClient{}}
	return ClientMux{clientsByKey: clients}
}

func clientSideHandler(allowedOrigins []string) ClientSideMux {
	envInfo := map[string]ClientSideInfo{key(): {allowedOrigins: allowedOrigins, client: &FakeLDClient{}}}
	return ClientSideMux{infoByKey: envInfo}
}

func buildRequest(verb string, vars map[string]string, headers map[string]string, body string) *http.Request {
	req, _ := http.NewRequest(verb, "", bytes.NewBuffer([]byte(body)))
	req = mux.SetURLVars(req, vars)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	ctx := clientContextImpl{client: &FakeLDClient{}}
	req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
	return req
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "")
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}`)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestAuthorizeMethodFailsOnInvalidAuthKey(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee", "Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}`)
	resp := httptest.NewRecorder()
	handler().selectClientByAuthorizationKey(func(http.ResponseWriter, *http.Request) { t.Fail() })(resp, req)

	assert.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}notjson`)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestFindEnvironmentFailsOnInvalidEnvId(t *testing.T) {
	vars := map[string]string{"envId": "blah", "user": user()}
	req := buildRequest("GET", vars, nil, "")
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).selectClientByUrlParam(evaluateAllFeatureFlags)(resp, req)

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

func TestOptionsHandlerSetsAllowHeader(t *testing.T) {
	method := "GET"
	req := buildRequest(method, nil, nil, "")
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).optionsHandler(method)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, resp.Header().Get("Allow"), method)
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildRequest("", nil, nil, "")
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "*")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Credentials"), "false")
		assert.Equal(t, w.Header().Get("Access-Control-Max-Age"), "300")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Methods"), "GET, REPORT")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type, Content-Length")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectDefaultHeadersWhenRequestHasOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "")
	resp := httptest.NewRecorder()

	clientSideHandler([]string{}).corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForSpecifiedDomain(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "")
	resp := httptest.NewRecorder()

	clientSideHandler([]string{"blah"}).corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForInvalidOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "")
	resp := httptest.NewRecorder()

	clientSideHandler([]string{"notblah"}).corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}
