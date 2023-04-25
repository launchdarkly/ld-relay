package middleware

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/browser"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testenv"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"

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

func buildPreRoutedRequestWithAuth(key credential.SDKCredential) *http.Request {
	headers := make(http.Header)
	headers.Set("Authorization", key.GetAuthorizationHeaderValue())
	return buildPreRoutedRequest("GET", nil, headers, nil, nil)
}

func buildPreRoutedRequestWithFilter(key credential.SDKCredential, filter config.FilterKey) *http.Request {
	req := buildPreRoutedRequestWithAuth(key)
	req.URL.RawQuery = url.Values{
		"filter": []string{string(filter)},
	}.Encode()
	return req
}

type testEnvironments struct {
	envs      map[sdkauth.ScopedCredential]relayenv.EnvContext
	notInited bool
}

var errNotReady = errors.New("not ready")
var errUnrecognized = errors.New("unrecognized environment")
var errPayloadFilterNotFound = errors.New("unrecognized payload filter")

func (t testEnvironments) GetEnvironment(c sdkauth.ScopedCredential) (relayenv.EnvContext, error) {
	if t.notInited {
		return nil, errNotReady
	}
	if e, ok := t.envs[c]; ok {
		return e, nil
	}
	if _, ok := t.envs[c.Unscope()]; ok {
		return nil, errPayloadFilterNotFound
	}
	return nil, errUnrecognized
}

func (t testEnvironments) IsNotReady(err error) bool {
	return err == errNotReady
}

func (t testEnvironments) IsPayloadFilterNotFound(err error) bool {
	return err == errPayloadFilterNotFound
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
	headers []string
}

func (c testCORSContext) AllowedOrigins() []string { return c.origins }
func (c testCORSContext) AllowedHeaders() []string { return c.headers }

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
		t.Run("unfiltered environment", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvMain.Config.SDKKey):   env1,
					sdkauth.New(st.EnvMobile.Config.SDKKey): env2,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)
			envCh := make(chan relayenv.EnvContext, 1)

			req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
			resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, env1, <-envCh)
		})

		t.Run("filtered environment", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.NewScoped("microservice-1", st.EnvMain.Config.SDKKey):   env1,
					sdkauth.NewScoped("microservice-1", st.EnvMobile.Config.SDKKey): env2,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)
			envCh := make(chan relayenv.EnvContext, 1)

			req := buildPreRoutedRequestWithFilter(st.EnvMain.Config.SDKKey, "microservice-1")
			resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, env1, <-envCh)
		})
	})

	t.Run("finds by mobile key", func(t *testing.T) {
		t.Run("unfiltered environment", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvMain.Config.SDKKey):      env1,
					sdkauth.New(st.EnvMobile.Config.SDKKey):    env2,
					sdkauth.New(st.EnvMobile.Config.MobileKey): env2,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)
			envCh := make(chan relayenv.EnvContext, 1)

			req := buildPreRoutedRequestWithAuth(st.EnvMobile.Config.MobileKey)
			resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, env2, <-envCh)
		})

		t.Run("filtered environment", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.NewScoped("microservice-1", st.EnvMain.Config.SDKKey):      env1,
					sdkauth.NewScoped("microservice-1", st.EnvMobile.Config.SDKKey):    env2,
					sdkauth.NewScoped("microservice-1", st.EnvMobile.Config.MobileKey): env2,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)
			envCh := make(chan relayenv.EnvContext, 1)

			req := buildPreRoutedRequestWithFilter(st.EnvMobile.Config.MobileKey, "microservice-1")
			resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, env2, <-envCh)
		})
	})

	t.Run("finds by combination of SDK key and filter key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvMain.Config.SDKKey):                         env1,
				sdkauth.NewScoped("microservice-1", st.EnvMain.Config.SDKKey): env2,
				sdkauth.NewScoped("microservice-2", st.EnvMain.Config.SDKKey): env1,
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env1, <-envCh)

		req = buildPreRoutedRequestWithFilter(st.EnvMain.Config.SDKKey, "microservice-1")
		resp, _ = st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)

		req = buildPreRoutedRequestWithFilter(st.EnvMain.Config.SDKKey, "microservice-2")
		resp, _ = st.DoRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env1, <-envCh)
	})

	t.Run("finds by environment ID in URL", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvMain.Config.SDKKey):       env1,
				sdkauth.New(st.EnvClientSide.Config.SDKKey): env2,
				sdkauth.New(st.EnvClientSide.Config.EnvID):  env2,
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
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{sdkauth.New(st.EnvMain.Config.SDKKey): env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("returns 404 if key is correct but filter is unrecognized", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvMain.Config.SDKKey): env1,
			},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req := buildPreRoutedRequestWithFilter(st.EnvMain.Config.SDKKey, "nonexistent-filter")
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("rejects unknown mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{sdkauth.New(st.EnvMain.Config.MobileKey): env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.UndefinedMobileKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown environment ID", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{sdkauth.New(st.EnvMain.Config.SDKKey): env1},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, envs)

		vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
		req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("rejects malformed SDK key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{sdkauth.New(st.MalformedSDKKey): testenv.NewTestEnvContext("server", false, nil)},
		}
		selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

		req1 := buildPreRoutedRequestWithAuth(st.MalformedSDKKey)
		resp1, _ := st.DoRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects malformed mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.MalformedSDKKey):    testenv.NewTestEnvContext("server", false, nil),
				sdkauth.New(st.MalformedMobileKey): testenv.NewTestEnvContext("server", false, nil),
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
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{sdkauth.New(st.EnvMain.Config.SDKKey): notReadyEnv},
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
	assert.Equal(t, browser.DefaultAllowedHeaders, resp.Result().Header.Get("Access-Control-Allow-Headers"))
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

func TestCORSMiddlewareSetsAllowedHeaderFromContext(t *testing.T) {
	cc := testCORSContext{headers: []string{"ghi", "jkl"}}
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	req = req.WithContext(browser.WithCORSContext(req.Context(), cc))
	resp := httptest.NewRecorder()

	CORS(nullHandler()).ServeHTTP(resp, req)

	expectedHeaders := browser.DefaultAllowedHeaders + ",ghi,jkl"
	assert.Equal(t, expectedHeaders, resp.Result().Header.Get("Access-Control-Allow-Headers"))
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

func TestCORSMiddlewareCallsWrappedHandlerWhenOriginMatchesAndMethodIsGET(t *testing.T) {
	totalTimesCalled := 0
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		totalTimesCalled++
		w.WriteHeader(200)
	})
	corsHandler := CORS(wrappedHandler)

	headers := make(http.Header)
	headers.Set("Origin", "blah")
	cc := testCORSContext{origins: []string{"abc", "blah"}}
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	req = req.WithContext(browser.WithCORSContext(req.Context(), cc))
	res := httptest.NewRecorder()
	corsHandler.ServeHTTP(res, req)
	assert.Equal(t, 200, res.Result().StatusCode)
	assert.Equal(t, "blah", res.Result().Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, 1, totalTimesCalled)
}

func TestStreaming(t *testing.T) {
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp := httptest.NewRecorder()

	Streaming(nullHandler()).ServeHTTP(resp, req)

	assert.Equal(t, "no", resp.Result().Header.Get("X-Accel-Buffering"))
}

func TestContextFromBase64(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		contextJSON := `{"kind":"org","key":"a","name":"b","c":true}`
		data := base64.StdEncoding.EncodeToString([]byte(contextJSON))
		expectedContext := ldcontext.NewBuilder("a").Kind("org").Name("b").SetBool("c", true).Build()
		context, err := ContextFromBase64(data)
		assert.NoError(t, err)
		assert.Equal(t, expectedContext, context)
	})

	t.Run("valid without padding", func(t *testing.T) {
		contextJSON := `{"kind":"org","key":"a","name":"b","c":true}`
		data0 := base64.StdEncoding.EncodeToString([]byte(contextJSON))
		data1 := strings.TrimRightFunc(data0, func(c rune) bool { return c == '=' })
		require.NotEqual(t, data0, data1)
		expectedContext := ldcontext.NewBuilder("a").Kind("org").Name("b").SetBool("c", true).Build()
		context, err := ContextFromBase64(data1)
		assert.NoError(t, err)
		assert.Equal(t, expectedContext, context)
	})

	t.Run("valid - old-style user", func(t *testing.T) {
		userJSON := `{"key":"a","name":"b","custom":{"c":true}}`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON))
		expectedContext := ldcontext.NewBuilder("a").Name("b").SetBool("c", true).Build()
		context, err := ContextFromBase64(data)
		assert.NoError(t, err)
		assert.Equal(t, expectedContext, context)
	})

	t.Run("invalid base64", func(t *testing.T) {
		contextJSON := `{"kind":"org","key":"a","name":"b","c":true}`
		data := base64.StdEncoding.EncodeToString([]byte(contextJSON)) + "x"
		_, err := ContextFromBase64(data)
		assert.Error(t, err)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		contextJSON := `{"sorry`
		data := base64.StdEncoding.EncodeToString([]byte(contextJSON))
		_, err := ContextFromBase64(data)
		assert.Error(t, err)
	})

	t.Run("user has no key", func(t *testing.T) {
		userJSON := `{"name":"n"}`
		data := base64.StdEncoding.EncodeToString([]byte(userJSON))
		_, err := ContextFromBase64(data)
		assert.Error(t, err)
	})
}
