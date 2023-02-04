package relay

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v7/config"
	st "github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	m "github.com/launchdarkly/go-test-helpers/v3/matchers"
)

// Test parameters for an endpoint that we want to test. The "data" parameter is used as the request body if
// the method is GET, and can also be included in base64 in the URL by putting "$DATA" in the URL path. Also,
// if the credential is an environment ID, it is substituted for "$ENV" in the URL path.
type endpointTestParams struct {
	name           string
	method         string
	path           string
	data           []byte
	credential     config.SDKCredential
	expectedStatus int
	bodyMatcher    m.Matcher
}

type endpointMultiTestParams struct {
	name       string
	method     string
	path       string
	credential config.SDKCredential
	requests   []endpointTestPerRequestParams
}

type endpointTestPerRequestParams struct {
	name           string
	data           []byte
	expectedStatus int
	bodyMatcher    m.Matcher
}

func makeEndpointTestPerRequestParams(userJSON []byte, contextJSON []byte, bodyMatcher m.Matcher) []endpointTestPerRequestParams {
	return []endpointTestPerRequestParams{
		{"user", userJSON, http.StatusOK, bodyMatcher},
		{"context", contextJSON, http.StatusOK, bodyMatcher},
	}
}

func (e endpointTestParams) toMulti() endpointMultiTestParams {
	return endpointMultiTestParams{
		name: e.name, method: e.method, path: e.path, credential: e.credential,
		requests: []endpointTestPerRequestParams{
			{"", e.data, e.expectedStatus, e.bodyMatcher},
		},
	}
}

func (e endpointTestParams) request() *http.Request {
	return e.toMulti().request(e.toMulti().requests[0])
}

func (e endpointMultiTestParams) request(r endpointTestPerRequestParams) *http.Request {
	return st.BuildRequest(e.method, e.localURL(r), r.data, e.header(r))
}

func (e endpointMultiTestParams) header(r endpointTestPerRequestParams) http.Header {
	h := make(http.Header)
	if e.credential != nil && e.credential.GetAuthorizationHeaderValue() != "" {
		h.Set("Authorization", e.credential.GetAuthorizationHeaderValue())
	}
	if r.data != nil && e.method != "GET" {
		h.Set("Content-Type", "application/json")
	}
	return h
}

func (e endpointTestParams) localURL() string {
	return e.toMulti().localURL(e.toMulti().requests[0])
}

func (e endpointMultiTestParams) localURL(r endpointTestPerRequestParams) string {
	p := e.path
	if strings.Contains(p, "$ENV") {
		if env, ok := e.credential.(config.EnvironmentID); ok {
			p = strings.ReplaceAll(p, "$ENV", string(env))
		} else {
			panic("test endpoint URL had $ENV but did not specify an environment ID")
		}
	}
	if strings.Contains(p, "$USER") {
		if r.data != nil {
			p = strings.ReplaceAll(p, "$USER", base64.StdEncoding.EncodeToString(r.data))
		} else {
			panic("test endpoint URL had $USER but did not specify any data")
		}
	}
	if strings.Contains(p, "$DATA") {
		if r.data != nil {
			p = strings.ReplaceAll(p, "$DATA", base64.StdEncoding.EncodeToString(r.data))
		} else {
			panic("test endpoint URL had $DATA but did not specify any data")
		}
	}
	if strings.Contains(p, "$") {
		panic("test endpoint URL had unrecognized format")
	}
	return "http://localhost" + p
}

// Test parameters for user data that should be rejected as invalid.
type badUserTestParams struct {
	name     string
	userJSON []byte
}

var allBadUserTestParams = []badUserTestParams{
	{"invalid user JSON", []byte(`{"key":"incomplete`)},
	{"missing user key", []byte(`{"name":"Keyless Joe"}`)},
}
