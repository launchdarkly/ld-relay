package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
)

func buildPreRoutedRequestWithAuth(key config.SDKCredential) *http.Request {
	headers := make(http.Header)
	headers.Set("Authorization", key.GetAuthorizationHeaderValue())
	return buildPreRoutedRequest("GET", nil, headers, nil, nil)
}

func TestSelectEnvironmentByAuthorizationKey(t *testing.T) {
	env1 := newTestEnvContext("env1", false, nil)
	env2 := newTestEnvContext("env2", false, nil)

	handlerThatDetectsEnvironment := func(outCh chan<- relayenv.EnvContext) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			outCh <- getClientContext(req).env
		})
	}

	t.Run("finds by SDK key", func(t *testing.T) {
		envs := testEnvironments{
			testEnvMain.config.SDKKey:   env1,
			testEnvMobile.config.SDKKey: env2,
		}
		selector := selectEnvironmentByAuthorizationKey(serverSdk, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(testEnvMain.config.SDKKey)
		resp, _ := doRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env1, <-envCh)
	})

	t.Run("finds by mobile key", func(t *testing.T) {
		envs := testEnvironments{
			testEnvMain.config.SDKKey:      env1,
			testEnvMobile.config.SDKKey:    env2,
			testEnvMobile.config.MobileKey: env2,
		}
		selector := selectEnvironmentByAuthorizationKey(mobileSdk, envs)
		envCh := make(chan relayenv.EnvContext, 1)

		req := buildPreRoutedRequestWithAuth(testEnvMobile.config.MobileKey)
		resp, _ := doRequest(req, selector(handlerThatDetectsEnvironment(envCh)))

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, env2, <-envCh)
	})

	t.Run("rejects unknown SDK key", func(t *testing.T) {
		envs := testEnvironments{testEnvMain.config.SDKKey: env1}
		selector := selectEnvironmentByAuthorizationKey(serverSdk, envs)

		req1 := buildPreRoutedRequestWithAuth(undefinedSDKKey)
		resp1, _ := doRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects unknown mobile key", func(t *testing.T) {
		envs := testEnvironments{testEnvMain.config.MobileKey: env1}
		selector := selectEnvironmentByAuthorizationKey(mobileSdk, envs)

		req1 := buildPreRoutedRequestWithAuth(undefinedMobileKey)
		resp1, _ := doRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects malformed SDK key", func(t *testing.T) {
		envs := testEnvironments{malformedSDKKey: newTestEnvContext("server", false, nil)}
		selector := selectEnvironmentByAuthorizationKey(serverSdk, envs)

		req1 := buildPreRoutedRequestWithAuth(malformedSDKKey)
		resp1, _ := doRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("rejects malformed mobile key", func(t *testing.T) {
		envs := testEnvironments{
			malformedSDKKey:    newTestEnvContext("server", false, nil),
			malformedMobileKey: newTestEnvContext("server", false, nil),
		}
		selector := selectEnvironmentByAuthorizationKey(mobileSdk, envs)

		req1 := buildPreRoutedRequestWithAuth(malformedMobileKey)
		resp1, _ := doRequest(req1, selector(nullHandler()))

		assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)
	})

	t.Run("returns 503 if client has not been created", func(t *testing.T) {
		notReadyEnv := newTestEnvContextWithClientFactory("env", clientFactoryThatFails(errors.New("sorry")), nil)
		envs := testEnvironments{testEnvMain.config.SDKKey: notReadyEnv}
		selector := selectEnvironmentByAuthorizationKey(serverSdk, envs)

		req := buildPreRoutedRequestWithAuth(testEnvMain.config.SDKKey)
		resp, _ := doRequest(req, selector(nullHandler()))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildPreRoutedRequest("GET", nil, nil, nil, nil)
	resp := httptest.NewRecorder()
	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}
