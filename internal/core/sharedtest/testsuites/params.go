package testsuites

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
)

// TestParams is information that is passed to test code with DoTest.
type TestParams struct {
	Core    *core.RelayCore
	Handler http.Handler
	Closer  func()
}

// TestConstructor is provided by whatever Relay variant is calling the test suite, to provide the appropriate
// setup and teardown for that variant.
type TestConstructor func(config.Config) TestParams

// RunTest is a shortcut for running a subtest method with this parameter.
func (c TestConstructor) RunTest(t *testing.T, name string, testFn func(*testing.T, TestConstructor)) {
	t.Run(name, func(t *testing.T) { testFn(t, c) })
}

// DoTest some code against a new Relay instance that is set up with the specified configuration.
func DoTest(c config.Config, constructor TestConstructor, action func(TestParams)) {
	p := constructor(c)
	defer p.Closer()
	action(p)
}

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
	bodyMatcher    st.BodyMatcher
}

func (e endpointTestParams) request() *http.Request {
	return st.BuildRequest(e.method, e.localURL(), e.data, e.header())
}

func (e endpointTestParams) header() http.Header {
	h := make(http.Header)
	if e.credential != nil && e.credential.GetAuthorizationHeaderValue() != "" {
		h.Set("Authorization", e.credential.GetAuthorizationHeaderValue())
	}
	if e.data != nil && e.method != "GET" {
		h.Set("Content-Type", "application/json")
	}
	return h
}

func (e endpointTestParams) localURL() string {
	p := e.path
	if strings.Contains(p, "$ENV") {
		if env, ok := e.credential.(config.EnvironmentID); ok {
			p = strings.ReplaceAll(p, "$ENV", string(env))
		} else {
			panic("test endpoint URL had $ENV but did not specify an environment ID")
		}
	}
	if strings.Contains(p, "$USER") {
		if e.data != nil {
			p = strings.ReplaceAll(p, "$USER", base64.StdEncoding.EncodeToString(e.data))
		} else {
			panic("test endpoint URL had $USER but did not specify any data")
		}
	}
	if strings.Contains(p, "$DATA") {
		if e.data != nil {
			p = strings.ReplaceAll(p, "$DATA", base64.StdEncoding.EncodeToString(e.data))
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
