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
	_, err = NewHTTPConfig(proxyConfig2, nil, "", ldlog.NewDisabledLoggers())
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)

	proxyConfig3 := proxyConfig2
	proxyConfig3.User = "user"
	_, err = NewHTTPConfig(proxyConfig3, nil, "", ldlog.NewDisabledLoggers())
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)

	proxyConfig4 := proxyConfig3
	proxyConfig4.Password = "pass"
	_, err = NewHTTPConfig(proxyConfig4, nil, "", ldlog.NewDisabledLoggers())
	assert.NoError(t, err)

	proxyConfig5 := proxyConfig4
	helpers.WithTempFile(func(certFileName string) {
		proxyConfig5.CACertFiles = configtypes.NewOptStringList([]string{certFileName})
		_, err = NewHTTPConfig(proxyConfig5, nil, "", ldlog.NewDisabledLoggers())
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "invalid CA certificate data")
		}
	})
}

func TestLogsRedactConnectionPassword(t *testing.T) {
	// Username and password are specified separately in NTLM auth won't show in logs as they're not part of server name
	url1, _ := configtypes.NewOptURLAbsoluteFromString("http://my-proxy")
	proxyConfig1 := config.ProxyConfig{NTLMAuth: true, URL: url1, User: "my-user", Password: "my-pass"}
	mockLog1 := ldlogtest.NewMockLog()
	_, err := NewHTTPConfig(proxyConfig1, nil, "", mockLog1.Loggers)
	assert.NoError(t, err)
	mockLog1.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://my-proxy$")

	// When username and password are configured as part of server name, verify the password is redacted
	url2, _ := url.Parse("http://my-user:my-password@my-proxy")
	url2Absolute, _ := configtypes.NewOptURLAbsolute(url2)
	proxyConfig2 := config.ProxyConfig{URL: url2Absolute}
	mockLog2 := ldlogtest.NewMockLog()
	_, err = NewHTTPConfig(proxyConfig2, nil, "", mockLog2.Loggers)
	assert.NoError(t, err)
	mockLog2.AssertMessageMatch(t, true, ldlog.Info, "Using proxy server at http://my-user:xxxxx@my-proxy$")
}
