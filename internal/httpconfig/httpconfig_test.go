package httpconfig

import (
	"context"
	"crypto/x509"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/launchdarkly/go-configtypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserAgentHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "abc", slog.Default())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Contains(t, headers.Get("User-Agent"), "abc")
}

func TestNoAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", slog.Default())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Equal(t, "", headers.Get("Authorization"))
}

func TestAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, config.SDKKey("key"), "", slog.Default())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.DefaultHeaders
	assert.Equal(t, "key", headers.Get("Authorization"))
}

func TestSimpleProxy(t *testing.T) {
	fakeURL := "http://fake-url/"
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(http.StatusOK))
	logger, mockHandler := logtest.NewMockLogger()

	httphelpers.WithServer(handler, func(server *httptest.Server) {
		proxyConfig := config.ProxyConfig{}
		proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		hc, err := NewHTTPConfig(proxyConfig, config.HTTPConfig{}, nil, "", logger)

		assertProxyLogMessage(t, mockHandler, server.URL)

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
	logger, mockHandler := logtest.NewMockLogger()

	httphelpers.WithSelfSignedServer(handler, func(server *httptest.Server, certData []byte, certPool *x509.CertPool) {
		helpers.WithTempFile(func(certFilePath string) {
			require.NoError(t, os.WriteFile(certFilePath, certData, 0))
			proxyConfig := config.ProxyConfig{}
			proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
			proxyConfig.CACertFiles = configtypes.NewOptStringList([]string{certFilePath})
			hc, err := NewHTTPConfig(proxyConfig, config.HTTPConfig{}, nil, "", logger)

			assertProxyLogMessage(t, mockHandler, server.URL)

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
	logger, _ := logtest.NewMockLogger()

	helpers.WithTempFile(func(certFilePath string) {
		proxyConfig := config.ProxyConfig{}
		proxyConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString("http://fake-proxy")
		proxyConfig.CACertFiles = configtypes.NewOptStringList([]string{certFilePath})
		_, err := NewHTTPConfig(proxyConfig, config.HTTPConfig{}, nil, "", logger)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "invalid CA certificate data")
		}
	})
}

func TestNTLMProxyInvalidConfigs(t *testing.T) {
	// The actual functioning of the NTLM proxy transport is tested in the SDK package where it is defined,
	// so here we're only testing that we validate the parameters correctly.

	proxyConfig1 := config.ProxyConfig{NTLMAuth: true}
	_, err := NewHTTPConfig(proxyConfig1, config.HTTPConfig{}, nil, "", slog.Default())
	assert.Equal(t, errProxyAuthWithoutProxyURL, err)

	proxyConfig2 := proxyConfig1
	proxyConfig2.URL, _ = configtypes.NewOptURLAbsoluteFromString("http://fake-proxy")
	_, err = NewHTTPConfig(proxyConfig2, config.HTTPConfig{}, nil, "", slog.Default())
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)

	proxyConfig3 := proxyConfig2
	proxyConfig3.User = "user"
	_, err = NewHTTPConfig(proxyConfig3, config.HTTPConfig{}, nil, "", slog.Default())
	assert.Equal(t, errNTLMProxyAuthWithoutCredentials, err)

	proxyConfig4 := proxyConfig3
	proxyConfig4.Password = "pass"
	_, err = NewHTTPConfig(proxyConfig4, config.HTTPConfig{}, nil, "", slog.Default())
	assert.NoError(t, err)

	proxyConfig5 := proxyConfig4
	helpers.WithTempFile(func(certFileName string) {
		proxyConfig5.CACertFiles = configtypes.NewOptStringList([]string{certFileName})
		_, err = NewHTTPConfig(proxyConfig5, config.HTTPConfig{}, nil, "", slog.Default())
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "invalid CA certificate data")
		}
	})
}

func TestLogsRedactConnectionPassword(t *testing.T) {
	// Username and password are specified separately in NTLM auth won't show in logs as they're not part of server name
	url1, _ := configtypes.NewOptURLAbsoluteFromString("http://my-proxy")
	proxyConfig1 := config.ProxyConfig{NTLMAuth: true, URL: url1, User: "my-user", Password: "my-pass"}
	logger1, mockHandler1 := logtest.NewMockLogger()
	_, err := NewHTTPConfig(proxyConfig1, config.HTTPConfig{}, nil, "", logger1)
	assert.NoError(t, err)
	assertProxyLogMessage(t, mockHandler1, "http://my-proxy")

	// When username and password are configured as part of server name, verify the password is redacted
	url2, _ := url.Parse("http://my-user:my-password@my-proxy")
	url2Absolute, _ := configtypes.NewOptURLAbsolute(url2)
	proxyConfig2 := config.ProxyConfig{URL: url2Absolute}
	logger2, mockHandler2 := logtest.NewMockLogger()
	_, err = NewHTTPConfig(proxyConfig2, config.HTTPConfig{}, nil, "", logger2)
	assert.NoError(t, err)
	assertProxyLogMessage(t, mockHandler2, "http://my-user:xxxxx@my-proxy")
}

// assertProxyLogMessage checks that the mock handler captured a "using proxy server" log entry
// at Info level with the expected URL in the "url" attribute.
func assertProxyLogMessage(t *testing.T, mockHandler *logtest.MockHandler, expectedURL string) {
	t.Helper()
	entries := mockHandler.EntriesForLevel(slog.LevelInfo)
	found := false
	for _, e := range entries {
		if e.Message == "using proxy server" {
			assert.Equal(t, expectedURL, e.Attrs["url"], "proxy URL mismatch in log entry")
			found = true
			break
		}
	}
	assert.True(t, found, "expected to find 'using proxy server' log entry at Info level")
}

// TestClientRecordsNoMetrics guards the meter provider passed to otelhttp. The instrumentation builds
// its own HTTP client instruments, and without a provider of its own it takes the global one. The
// global provider is a no-op that delegates retroactively, so those instruments would come to life the
// moment any code called otel.SetMeterProvider. Relay wraps the transport for trace context
// propagation only; reporting outbound HTTP metrics should be a deliberate decision.
func TestClientRecordsNoMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetMeterProvider(previous)
	})

	handler, _ := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(http.StatusOK))
	server := httptest.NewServer(handler)
	defer server.Close()

	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "abc", slog.Default())
	require.NoError(t, err)

	resp, err := hc.Client().Get(server.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))

	for _, scope := range collected.ScopeMetrics {
		if scope.Scope.Name != otelhttp.ScopeName {
			continue
		}
		for _, m := range scope.Metrics {
			assert.Fail(t, "otelhttp recorded a metric through the global meter provider",
				"scope %q reported %q", scope.Scope.Name, m.Name)
		}
	}

	// A canary proves the reader would have seen a metric, so the assertion above is not vacuous.
	counter, err := provider.Meter("canary").Int64Counter("canary.requests")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.NotEmpty(t, collected.ScopeMetrics, "the manual reader collected nothing at all")
}
