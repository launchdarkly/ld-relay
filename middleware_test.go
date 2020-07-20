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

func TestClientMuxRejectsMalformedSDKKeyOrMobileKey(t *testing.T) {
	mux := clientMux{
		clientContextByKey: map[config.SDKCredential]relayenv.EnvContext{
			malformedSDKKey:    newTestEnvContext("server", false, nil),
			malformedMobileKey: newTestEnvContext("mobile", false, nil),
		},
	}

	req1 := buildPreRoutedRequestWithAuth(malformedSDKKey)
	resp1, _ := doRequest(req1, mux.selectClientByAuthorizationKey(serverSdk)(nullHandler()))

	assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)

	req2 := buildPreRoutedRequestWithAuth(malformedMobileKey)
	resp2, _ := doRequest(req2, mux.selectClientByAuthorizationKey(serverSdk)(nullHandler()))

	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

func TestClientMuxRejectsUnknownSDKKeyOrMobileKey(t *testing.T) {
	mux := clientMux{}

	req1 := buildPreRoutedRequestWithAuth(undefinedSDKKey)
	resp1, _ := doRequest(req1, mux.selectClientByAuthorizationKey(serverSdk)(nullHandler()))

	assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)

	req2 := buildPreRoutedRequestWithAuth(undefinedMobileKey)
	resp2, _ := doRequest(req2, mux.selectClientByAuthorizationKey(serverSdk)(nullHandler()))

	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

func TestClientMuxReturns503IfClientHasNotBeenCreated(t *testing.T) {
	ctx := newTestEnvContextWithClientFactory("env", clientFactoryThatFails(errors.New("sorry")), nil)
	serverSideMux := clientMux{
		clientContextByKey: map[config.SDKCredential]relayenv.EnvContext{
			testEnvMain.config.SDKKey: ctx,
		},
	}
	mobileMux := clientMux{
		clientContextByKey: map[config.SDKCredential]relayenv.EnvContext{
			testEnvMobile.config.MobileKey: ctx,
		},
	}

	req1 := buildPreRoutedRequestWithAuth(testEnvMain.config.SDKKey)
	resp1, _ := doRequest(req1, serverSideMux.selectClientByAuthorizationKey(serverSdk)(nullHandler()))

	assert.Equal(t, http.StatusServiceUnavailable, resp1.StatusCode)

	req2 := buildPreRoutedRequestWithAuth(testEnvMobile.config.MobileKey)
	resp2, _ := doRequest(req2, mobileMux.selectClientByAuthorizationKey(mobileSdk)(nullHandler()))

	assert.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode)
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
