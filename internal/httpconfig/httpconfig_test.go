package httpconfig

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserAgentHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, nil, "abc", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Contains(t, headers.Get("User-Agent"), "abc")
}

func TestNoAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Equal(t, "", headers.Get("Authorization"))
}

func TestAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.SDKKey("key"), "", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Equal(t, "key", headers.Get("Authorization"))
}

func TestSimpleProxy(t *testing.T) {
	fakeURL := "http://fake-url/"
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(http.StatusOK))
	mockLog := ldlogtest.NewMockLog()

	httphelpers.WithServer(handler, func(server *httptest.Server) {
		proxyConfig := config.ProxyConfig{}
		proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		hc, err := NewHTTPConfig(proxyConfig, nil, "", mockLog.Loggers)

		mockLog.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at "+server.URL)

		client := hc.Client()
		resp, err := client.Get(fakeURL)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		req := <-requestsCh
		assert.Equal(t, fakeURL, req.Request.URL.String())
	})
}

func TestSimpleProxyWithCACert(t *testing.T) {
	fakeURL := "http://fake-url/"
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(http.StatusOK))
	mockLog := ldlogtest.NewMockLog()

	httphelpers.WithSelfSignedServer(handler, func(server *httptest.Server, certData []byte, certPool *x509.CertPool) {
		helpers.WithTempFile(func(certFilePath string) {
			require.NoError(t, os.WriteFile(certFilePath, certData, 0))
			proxyConfig := config.ProxyConfig{}
			proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
			proxyConfig.CACertFiles = configtypes.NewOptStringList([]string{certFilePath})
			hc, err := NewHTTPConfig(proxyConfig, nil, "", mockLog.Loggers)

			mockLog.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at "+server.URL)

			client := hc.Client()
			resp, err := client.Get(fakeURL)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)

			req := <-requestsCh
			assert.Equal(t, fakeURL, req.Request.URL.String())
		})
	})
}

func TestSimpleProxyCACertError(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()

	helpers.WithTempFile(func(certFilePath string) {
		proxyConfig := config.ProxyConfig{}
		proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString("http://fake-proxy")
		proxyConfig.CACertFiles = configtypes.NewOptStringList([]string{certFilePath})
		_, err := NewHTTPConfig(proxyConfig, nil, "", mockLog.Loggers)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "invalid CA certificate data")
		}
	})
}

func TestNTLMProxyInvalidConfigs(t *testing.T) {
	// The actual functioning of the NTLM proxy transport is tested in the SDK package where it is defined,
	// so here we're only testing that we validate the parameters correctly.

	proxyConfig1 := config.ProxyConfig{NTLMAuth: true}
	_, err := NewHTTPConfig(proxyConfig1, nil, "", ldlog.NewDisabledLoggers())
	assert.Equal(t, errProxyAuthWithoutProxyURL, err)

	proxyConfig2 := proxyConfig1
	proxyConfig2.URL, _ = configtypes.NewOptURLAbsoluteFromString("http://fake-proxy")
	mockLog2 := ldlogtest.NewMockLog()
	_, err = NewHTTPConfig(proxyConfig2, nil, "", mockLog2.Loggers)
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)
	mockLog2.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://fake-proxy$")

	proxyConfig3 := proxyConfig2
	proxyConfig3.User = "user"
	mockLog3 := ldlogtest.NewMockLog()
	_, err = NewHTTPConfig(proxyConfig3, nil, "", mockLog3.Loggers)
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)
	mockLog3.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://fake-proxy$")

	proxyConfig4 := proxyConfig3
	proxyConfig4.Password = "pass"
	mockLog4 := ldlogtest.NewMockLog()
	_, err = NewHTTPConfig(proxyConfig4, nil, "", mockLog4.Loggers)
	assert.NoError(t, err)
	mockLog4.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://fake-proxy$")

	proxyConfig5 := proxyConfig4
	helpers.WithTempFile(func(certFileName string) {
		proxyConfig5.CACertFiles = configtypes.NewOptStringList([]string{certFileName})
		mockLog5 := ldlogtest.NewMockLog()
		_, err = NewHTTPConfig(proxyConfig5, nil, "", mockLog5.Loggers)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "invalid CA certificate data")
		}
		mockLog5.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://fake-proxy$")
	})

	proxyConfig6 := proxyConfig4
	url6, _ := url.Parse("http://my-user:my-password@my-proxy")
	proxyConfig6.URL, _ = configtypes.NewOptURLAbsolute(url6)
	mockLog6 := ldlogtest.NewMockLog()
	_, err = NewHTTPConfig(proxyConfig6, nil, "", mockLog6.Loggers)
	assert.NoError(t, err)
	mockLog6.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://my-user:xxxxx@my-proxy$")

}
