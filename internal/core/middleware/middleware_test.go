package middleware

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/browser"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testenv"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shortcut for building a request when we are going to be passing it directly to an endpoint handler, rather than
// going through the usual routing mechanism, so we must provide the Context and the URL path variables explicitly.
func buildPreRoutedRequest(verb string, body []byte, headers http.Header, vars map[string]string, ctx relayenv.EnvContext) *http.Request {
	req := st.BuildRequest(verb, "", body, headers)
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

type testEnvironments struct {
	envs      map[config.SDKCredential]relayenv.EnvContext
	notInited bool
}

func (t testEnvironments) GetEnvironment(c config.SDKCredential) (relayenv.EnvContext, bool) {
	return t.envs[c], !t.notInited
}

func (t testEnvironments) GetAllEnvironments() []relayenv.EnvContext {
	var ret []relayenv.EnvContext
	for _, e := range t.envs {
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

type testCORSContext struct {
	origins []string
}

func (c testCORSContext) AllowedOrigins() []string { return c.origins }

func nullHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
}

func TestChain(t *testing.T) {
	result := ""
	mw1 := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result += "1"
			h.ServeHTTP(w, r)
		})
	}
	mw2 := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result += "2"
			h.ServeHTTP(w, r)
		})
	}
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "", nil)
	Chain(mw1, mw2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result += "3"
	})).ServeHTTP(rr, req)
	assert.Equal(t, "123", result)
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
			envs: map[config.SDKCredential]relayenv.EnvContext{
				st.EnvMain.Config.SDKKey:   env1,
				st.EnvMobile.Config.SDKKey: env2,
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env1, <-envCh)
	})

	t.Run("finds by mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{
				st.EnvMain.Config.SDKKey:      env1,
				st.EnvMobile.Config.SDKKey:    env2,
				st.EnvMobile.Config.MobileKey: env2,
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(st.EnvMobile.Config.MobileKey)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)
	})

	t.Run("finds by environment ID in URL", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{
				st.EnvMain.Config.SDKKey:       env1,
				st.EnvClientSide.Config.SDKKey: env2,
				st.EnvClientSide.Config.EnvID:  env2,
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
		req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)
	})

	t.Run("rejects unknown SDK key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{st.EnvMain.Config.SDKKey: env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{st.EnvMain.Config.MobileKey: env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedMobileKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown environment ID", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{st.EnvMain.Config.SDKKey: env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, envs)

		vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
		req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("rejects malformed SDK key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{st.MalformedSDKKey: testenv.NewTestEnvContext("server", false, nil)},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.MalformedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects malformed mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{
				st.MalformedSDKKey:    testenv.NewTestEnvContext("server", false, nil),
				st.MalformedMobileKey: testenv.NewTestEnvContext("server", false, nil),
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.MalformedMobileKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("returns 503 if client has not been created", func(t *testing.T) {
		notReadyEnv := testenv.NewTestEnvContextWithClientFactory("env", testclient.ClientFactoryThatFails(errors.New("sorry")), nil)
		envs := testEnvironments{
			envs: map[config.SDKCredential]relayenv.EnvContext{st.EnvMain.Config.SDKKey: notReadyEnv},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})

	t.Run("returns 503 if Relay has not been initialized", func(t *testing.T) {
		envs := testEnvironments{notInited: true}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

func TestCORSMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp := httptest.NewRecorder()

	CORS(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "*", resp.Result().Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "false", resp.Result().Header.Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "300", resp.Result().Header.Get("Access-Control-Max-Age"))
	assert.Equal(t, "Cache-Control,Content-Type,Content-Length,Accept-Encoding,X-LaunchDarkly-User-Agent,X-LaunchDarkly-Payload-ID,X-LaunchDarkly-Wrapper,"+events.EventSchemaHeader,
		resp.Result().Header.Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "Date", resp.Result().Header.Get("Access-Control-Expose-Headers"))
}

func TestCORSMiddlewareSetsCorrectDefaultHeadersWhenRequestHasOrigin(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Origin", "blah")
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	resp := httptest.NewRecorder()

	CORS(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "blah", resp.Result().Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddlewareSetsAllowedOriginFromContextWhenOriginMatches(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Origin", "def")
	cc := testCORSContext{origins: []string{"abc", "def"}}
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	req = req.WithContext(browser.WithCORSContext(req.Context(), cc))
	resp := httptest.NewRecorder()

	CORS(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "def", resp.Result().Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddlewareSetsAllowedOriginFromContextWhenOriginDoesNotMatch(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Origin", "blah")
	cc := testCORSContext{origins: []string{"abc", "def"}}
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	req = req.WithContext(browser.WithCORSContext(req.Context(), cc))
	resp := httptest.NewRecorder()

	CORS(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "abc", resp.Result().Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddlewareOnlyCallsWrappedHandlerIfMethodIsNotOPTIONS(t *testing.T) {
	totalTimesCalled := 0
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		totalTimesCalled++
		w.WriteHeader(200)
	})
	corsHandler := CORS(wrappedHandler)

	req1 := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp1 := httptest.NewRecorder()
	corsHandler.ServeHTTP(resp1, req1)
	assert.Equal(t, 200, resp1.Result().StatusCode)
	assert.Equal(t, 1, totalTimesCalled)

	headers := make(http.Header)
	headers.Set("Origin", "blah")
	req2 := buildPreRoutedRequest("OPTIONS", nil, headers, nil, nil)
	resp2 := httptest.NewRecorder()
	corsHandler.ServeHTTP(resp2, req2)
	assert.Equal(t, 200, resp2.Result().StatusCode)
	assert.Equal(t, "blah", resp2.Result().Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, 1, totalTimesCalled) // wrappedHandler was not called this time
}

func TestStreaming(t *testing.T) {
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp := httptest.NewRecorder()

	Streaming(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "no", resp.Result().Header.Get("X-Accel-Buffering"))
}

func TestUserFromBase64(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		userJSON := `{"key":"user-key","name":"n","custom":{"good":true}}`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON))
		expectedUser := lduser.NewUserBuilder("user-key").Name("n").Custom("good", ldvalue.Bool(true)).Build()
		user, err := UserFromBase64(data)
		assert.NoError(t, err)
		assert.Equal(t, expectedUser, user)
	})

	t.Run("valid without padding", func(t *testing.T) {
		userJSON := `{"key":"user-key","name":"n","custom":{"good":true}}`
		data0 := base64.StdEncoding.EncodeToString([]byte(userJSON))
		data1 := strings.TrimRightFunc(data0, func(c rune) bool { return c == '=' })
		require.NotEqual(t, data0, data1)
		expectedUser := lduser.NewUserBuilder("user-key").Name("n").Custom("good", ldvalue.Bool(true)).Build()
		user, err := UserFromBase64(data1)
		assert.NoError(t, err)
		assert.Equal(t, expectedUser, user)
	})

	t.Run("invalid base64", func(t *testing.T) {
		userJSON := `{"key":"user-key","name":"n","custom":{"good":true}}`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON)) + "x"
		_, err := UserFromBase64(data)
		assert.Error(t, err)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		userJSON := `{"sorry`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON))
		_, err := UserFromBase64(data)
		assert.Error(t, err)
	})

	t.Run("user has no key", func(t *testing.T) {
		userJSON := `{"name":"n"}`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON))
		_, err := UserFromBase64(data)
		assert.Error(t, err)
	})
}
