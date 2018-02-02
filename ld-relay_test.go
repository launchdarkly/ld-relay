package main

import (
	"fmt"
	"github.com/gorilla/mux"
	"github.com/streamrail/concurrent-map"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"bytes"
	ld "gopkg.in/launchdarkly/go-client.v2"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
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

func handler() muxHandler {
	clients := cmap.New()
	clients.Set(key(), &FakeLDClient{})
	return muxHandler{clients: clients}
}

func testMethod(verb string, vars map[string]string, headers map[string]string, body string, method func(w http.ResponseWriter, r *http.Request)) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(verb, "", bytes.NewBuffer([]byte(body)))
	mux.SetURLVars(req, vars)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp := httptest.NewRecorder()
	method(resp, req)
	return resp
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": key()}
	resp := testMethod("GET", vars, headers, "", handler().authorizeEval(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": key(), "Content-Type": "application/json"}
	resp := testMethod("REPORT", vars, headers, `{"user":"key"}`, handler().authorizeEval(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestFlagEvalFailsOnInvalidAuthKey(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee", "Content-Type": "application/json"}
	resp := testMethod("REPORT", vars, headers, `{"user":"key"}`, handler().authorizeEval(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": key(), "Content-Type": "application/json"}
	resp := testMethod("REPORT", vars, headers, `{"user":"key"}notjson`, handler().authorizeEval(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestClientSideFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"envId": key(), "user": user()}
	resp := testMethod("GET", vars, nil, "", handler().findEnvironment(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestClientSideFlagEvalFailsOnInvalidEnvId(t *testing.T) {
	vars := map[string]string{"envId": "blah", "user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	resp := testMethod("GET", vars, headers, "", handler().findEnvironment(evaluateAllFeatureFlags))

	assert.Equal(t, http.StatusNotFound, resp.Code)
}
