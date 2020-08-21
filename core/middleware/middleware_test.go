package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testenv"
)

// Shortcut for building a request when we are going to be passing it directly to an endpoint handler, rather than
// going through the usual routing mechanism, so we must provide the Context and the URL path variables explicitly.
func buildPreRoutedRequest(verb string, body []byte, headers http.Header, vars map[string]string, ctx relayenv.EnvContext) *http.Request {
	req := sharedtest.BuildRequest(verb, "", body, headers)
	req = mux.SetURLVars(req, vars)
	if ctx != nil {
		req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: ctx}))
	}
	return req
}

func buildPreRoutedRequestWithAuth(key config.SDKCredential) *http.Request {
	headers := make(http.Header)
	headers.Set("Authorization", key.GetAuthorizationHeaderValue())
	return buildPreRoutedRequest("GET", nil, headers, nil, nil)
}

type testEnvironments map[config.SDKCredential]relayenv.EnvContext

func (t testEnvironments) GetEnvironment(c config.SDKCredential) relayenv.EnvContext {
	return t[c]
}

func (t testEnvironments) GetAllEnvironments() []relayenv.EnvContext {
	var ret []relayenv.EnvContext
	for _, e := range t {
		exists := false
		for _, e1 := range ret {
			if e1 == e {
				exists = true
				break
			}
		}
		if !exists {
			ret = append(ret, e)
		}
	}
	return ret
}

func nullHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
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

func TestSelectEnvironmentByAuthorizationKey(t *testing.T) {
	env1 := testenv.NewTestEnvContext("env1", false, nil)
	env2 := testenv.NewTestEnvContext("env2", false, nil)

	handlerThatDetectsEnvironment := func(outCh chan<- relayenv.EnvContext) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			outCh <- GetEnvContextInfo(req.Context()).Env
		})
	}

	t.Run("finds by SDK key", func(t *testing.T) {
		envs := testEnvironments{
			st.EnvMain.Config.SDKKey:   env1,
			st.EnvMobile.Config.SDKKey: env2,
		}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Server, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env1, <-envCh)
	})

	t.Run("finds by mobile key", func(t *testing.T) {
		envs := testEnvironments{
			st.EnvMain.Config.SDKKey:      env1,
			st.EnvMobile.Config.SDKKey:    env2,
			st.EnvMobile.Config.MobileKey: env2,
		}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Mobile, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(st.EnvMobile.Config.MobileKey)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)
	})

	t.Run("finds by environment ID in URL", func(t *testing.T) {
		envs := testEnvironments{
			st.EnvMain.Config.SDKKey:       env1,
			st.EnvClientSide.Config.SDKKey: env2,
			st.EnvClientSide.Config.EnvID:  env2,
		}
		selector := SelectEnvironmentByAuthorizationKey(sdks.JSClient, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
		req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)
	})

	t.Run("rejects unknown SDK key", func(t *testing.T) {
		envs := testEnvironments{st.EnvMain.Config.SDKKey: env1}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Server, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown mobile key", func(t *testing.T) {
		envs := testEnvironments{st.EnvMain.Config.MobileKey: env1}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Mobile, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedMobileKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown environment ID", func(t *testing.T) {
		envs := testEnvironments{st.EnvMain.Config.SDKKey: env1}
		selector := SelectEnvironmentByAuthorizationKey(sdks.JSClient, envs)

		vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
		req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("rejects malformed SDK key", func(t *testing.T) {
		envs := testEnvironments{st.MalformedSDKKey: testenv.NewTestEnvContext("server", false, nil)}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Server, envs)

		req1 := buildPreRoutedRequestWithAuth(st.MalformedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects malformed mobile key", func(t *testing.T) {
		envs := testEnvironments{
			st.MalformedSDKKey:    testenv.NewTestEnvContext("server", false, nil),
			st.MalformedMobileKey: testenv.NewTestEnvContext("server", false, nil),
		}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Mobile, envs)

		req1 := buildPreRoutedRequestWithAuth(st.MalformedMobileKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("returns 503 if client has not been created", func(t *testing.T) {
		notReadyEnv := testenv.NewTestEnvContextWithClientFactory("env", testclient.ClientFactoryThatFails(errors.New("sorry")), nil)
		envs := testEnvironments{st.EnvMain.Config.SDKKey: notReadyEnv}
		selector := SelectEnvironmentByAuthorizationKey(sdks.Server, envs)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp := httptest.NewRecorder()
	CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "*")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Credentials"), "false")
		assert.Equal(t, w.Header().Get("Access-Control-Max-Age"), "300")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Cache-Control,Content-Type,Content-Length,Accept-Encoding,X-LaunchDarkly-User-Agent,X-LaunchDarkly-Payload-ID,X-LaunchDarkly-Wrapper,"+events.EventSchemaHeader)
		assert.Equal(t, w.Header().Get("Access-Control-Expose-Headers"), "Date")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectDefaultHeadersWhenRequestHasOrigin(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Origin", "blah")
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	resp := httptest.NewRecorder()

	CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}
