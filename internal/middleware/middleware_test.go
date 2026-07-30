package middleware

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/internal/credential"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/browser"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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

var (
	errNotReady              = errors.New("not ready")
	errUnrecognized          = errors.New("unrecognized environment")
	errPayloadFilterNotFound = errors.New("unrecognized payload filter")
)

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
	t.Run("returns empty string when no user-agent headers are present", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		assert.Equal(t, "", getUserAgent(req))
	})
}

func TestGetSDKWrapper(t *testing.T) {
	t.Run("returns X-LaunchDarkly-Wrapper header value", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set(ldWrapperHeader, "react/2.0.0")
		assert.Equal(t, "react/2.0.0", getSDKWrapper(req))
	})
	t.Run("returns empty string when X-LaunchDarkly-Wrapper header is not present", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		assert.Equal(t, "", getSDKWrapper(req))
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

func TestSelectEnvironmentByClientSideAuth(t *testing.T) {
	envWithAllCreds := testenv.NewTestEnvContextWithEnvConfig("env-all", st.EnvWithAllCredentials.Config, false, nil)

	t.Run("finds by environment ID", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvWithAllCredentials.Config.EnvID): envWithAllCreds,
			},
		}
		selector := SelectEnvironmentByClientSideAuth(envs)

		headers := make(http.Header)
		headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.EnvID))
		req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("finds by mobile key", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvWithAllCredentials.Config.MobileKey): envWithAllCreds,
			},
		}
		selector := SelectEnvironmentByClientSideAuth(envs)

		headers := make(http.Header)
		headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.MobileKey))
		req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("skips auth for OPTIONS preflight", func(t *testing.T) {
		envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{}}
		selector := SelectEnvironmentByClientSideAuth(envs)

		req := buildPreRoutedRequest("OPTIONS", nil, nil, nil, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("rejects unknown credential", func(t *testing.T) {
		envs := testEnvironments{
			envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
				sdkauth.New(st.EnvWithAllCredentials.Config.EnvID): envWithAllCreds,
			},
		}
		selector := SelectEnvironmentByClientSideAuth(envs)

		headers := make(http.Header)
		headers.Set("Authorization", string(st.UndefinedEnvID))
		req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("returns 503 if Relay has not been initialized", func(t *testing.T) {
		envs := testEnvironments{notInited: true}
		selector := SelectEnvironmentByClientSideAuth(envs)

		headers := make(http.Header)
		headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.EnvID))
		req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
		resp, _ := st.DoRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

// installSpanRecorder installs an in-memory span recorder as the global tracer
// provider for the duration of the test, so assertions can be made about spans
// produced by tracing.Tracer(). Because the tracer provider is process-global,
// tests using this must not call t.Parallel.
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

func assertSpanHasStringAttribute(t *testing.T, span sdktrace.ReadOnlySpan, key attribute.Key, value string) {
	t.Helper()
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			assert.Equal(t, value, kv.Value.AsString())
			return
		}
	}
	t.Errorf("span %q is missing attribute %q", span.Name(), key)
}

// authSpanObserver returns a handler that records, at the moment it is invoked,
// whether the auth span has already ended and what span (if any) is active in
// the request context. This is how we verify the auth span is closed before the
// next handler runs, rather than encompassing downstream handling.
func authSpanObserver(sr *tracetest.SpanRecorder, endedDuringNext *bool, activeInNext *trace.SpanContext) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		*endedDuringNext = slices.ContainsFunc(sr.Ended(), func(s sdktrace.ReadOnlySpan) bool {
			return s.Name() == tracing.SpanAuth
		})
		*activeInNext = trace.SpanContextFromContext(req.Context())
	})
}

func TestSelectEnvironmentByAuthorizationKeyEndsAuthSpanBeforeNext(t *testing.T) {
	sr := installSpanRecorder(t)

	env1 := testenv.NewTestEnvContext("env1", false, nil)
	envs := testEnvironments{
		envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
			sdkauth.New(st.EnvMain.Config.SDKKey): env1,
		},
	}
	selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

	var authEndedDuringNext bool
	var activeInNext trace.SpanContext
	next := authSpanObserver(sr, &authEndedDuringNext, &activeInNext)

	req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
	resp, _ := st.DoRequest(req, selector(next))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.True(t, authEndedDuringNext, "auth span must end before the next handler runs")

	ended := sr.Ended()
	require.Len(t, ended, 1)
	authSpan := ended[0]
	assert.Equal(t, tracing.SpanAuth, authSpan.Name())
	assertSpanHasStringAttribute(t, authSpan, tracing.AuthResultKey, "success")
	assertSpanHasStringAttribute(t, authSpan, tracing.SDKKindKey, string(basictypes.ServerSDK))

	// The next handler must not run scoped to the (now-ended) auth span; it
	// should carry the original request context instead.
	assert.NotEqual(t, authSpan.SpanContext().SpanID(), activeInNext.SpanID())
}

func TestSelectEnvironmentByClientSideAuthEndsAuthSpanBeforeNext(t *testing.T) {
	sr := installSpanRecorder(t)

	envWithAllCreds := testenv.NewTestEnvContextWithEnvConfig("env-all", st.EnvWithAllCredentials.Config, false, nil)
	envs := testEnvironments{
		envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
			sdkauth.New(st.EnvWithAllCredentials.Config.MobileKey): envWithAllCreds,
		},
	}
	selector := SelectEnvironmentByClientSideAuth(envs)

	var authEndedDuringNext bool
	var activeInNext trace.SpanContext
	next := authSpanObserver(sr, &authEndedDuringNext, &activeInNext)

	headers := make(http.Header)
	headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.MobileKey))
	req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
	resp, _ := st.DoRequest(req, selector(next))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.True(t, authEndedDuringNext, "auth span must end before the next handler runs")

	ended := sr.Ended()
	require.Len(t, ended, 1)
	authSpan := ended[0]
	assert.Equal(t, tracing.SpanAuth, authSpan.Name())
	assertSpanHasStringAttribute(t, authSpan, tracing.AuthResultKey, "success")
	assertSpanHasStringAttribute(t, authSpan, tracing.SDKKindKey, string(basictypes.MobileSDK))

	// The next handler must not run scoped to the (now-ended) auth span; it
	// should carry the original request context instead.
	assert.NotEqual(t, authSpan.SpanContext().SpanID(), activeInNext.SpanID())
}

func TestEnvIDHeader(t *testing.T) {
	const testEnvID = config.EnvironmentID("507f1f77bcf86cd79943902a")

	envWithEnvID := testenv.NewTestEnvContextWithEnvConfig("env-with-id", config.EnvConfig{
		EnvID: testEnvID,
	}, false, nil)

	envWithoutEnvID := testenv.NewTestEnvContext("env-without-id", false, nil)

	t.Run("SelectEnvironmentByAuthorizationKey", func(t *testing.T) {
		t.Run("sets header for server-side SDK when env has env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvMain.Config.SDKKey): envWithEnvID,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

			req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, string(testEnvID), resp.Header.Get(ldEnvIDHeader))
		})

		t.Run("sets header for mobile SDK when env has env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvMobile.Config.MobileKey): envWithEnvID,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, envs)

			req := buildPreRoutedRequestWithAuth(st.EnvMobile.Config.MobileKey)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, string(testEnvID), resp.Header.Get(ldEnvIDHeader))
		})

		t.Run("sets header for JS client SDK when env has env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvClientSide.Config.EnvID): envWithEnvID,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, envs)

			vars := map[string]string{"envId": string(st.EnvClientSide.Config.EnvID)}
			req := buildPreRoutedRequest("GET", nil, nil, vars, nil)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, string(testEnvID), resp.Header.Get(ldEnvIDHeader))
		})

		t.Run("does not set header when env has no env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvMain.Config.SDKKey): envWithoutEnvID,
				},
			}
			selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

			req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "", resp.Header.Get(ldEnvIDHeader))
		})
	})

	t.Run("SelectEnvironmentByClientSideAuth", func(t *testing.T) {
		t.Run("sets header when authenticating with env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvWithAllCredentials.Config.EnvID): envWithEnvID,
				},
			}
			selector := SelectEnvironmentByClientSideAuth(envs)

			headers := make(http.Header)
			headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.EnvID))
			req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, string(testEnvID), resp.Header.Get(ldEnvIDHeader))
		})

		t.Run("sets header when authenticating with mobile key", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvWithAllCredentials.Config.MobileKey): envWithEnvID,
				},
			}
			selector := SelectEnvironmentByClientSideAuth(envs)

			headers := make(http.Header)
			headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.MobileKey))
			req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, string(testEnvID), resp.Header.Get(ldEnvIDHeader))
		})

		t.Run("does not set header when env has no env ID", func(t *testing.T) {
			envs := testEnvironments{
				envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
					sdkauth.New(st.EnvWithAllCredentials.Config.MobileKey): envWithoutEnvID,
				},
			}
			selector := SelectEnvironmentByClientSideAuth(envs)

			headers := make(http.Header)
			headers.Set("Authorization", string(st.EnvWithAllCredentials.Config.MobileKey))
			req := buildPreRoutedRequest("GET", nil, headers, nil, nil)
			resp, _ := st.DoRequest(req, selector(nullHandler()))

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "", resp.Header.Get(ldEnvIDHeader))
		})
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
	assert.Equal(t, "Date,X-LD-EnvId", resp.Result().Header.Get("Access-Control-Expose-Headers"))
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

func TestAuthEnvSpanAttributes(t *testing.T) {
	t.Run("sanitizes the display name", func(t *testing.T) {
		env := testenv.NewTestEnvContext("My Project/My Env", false, nil)

		attrs := authEnvSpanAttributes(env)

		require.Len(t, attrs, 1)
		assert.Equal(t, tracing.AuthEnvNameKey, attrs[0].Key)
		assert.Equal(t, "My Project_My Env", attrs[0].Value.AsString())
	})

	t.Run("includes the environment ID when one is configured", func(t *testing.T) {
		const testEnvID = config.EnvironmentID("507f1f77bcf86cd79943902a")
		env := testenv.NewTestEnvContextWithEnvConfig("env-with-id", config.EnvConfig{
			EnvID: testEnvID,
		}, false, nil)

		attrs := authEnvSpanAttributes(env)

		require.Len(t, attrs, 2)
		assert.Equal(t, tracing.AuthEnvNameKey, attrs[0].Key)
		assert.Equal(t, "env-with-id", attrs[0].Value.AsString())
		assert.Equal(t, tracing.AuthEnvIDKey, attrs[1].Key)
		assert.Equal(t, string(testEnvID), attrs[1].Value.AsString())
	})
}
