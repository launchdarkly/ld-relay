// Package httpconfig provides helpers for special types of HTTP client configuration supported by Relay.
package httpconfig

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/ldhttp"
	"github.com/launchdarkly/go-server-sdk/v7/ldntlm"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

var (
	errNTLMProxyAuthWithoutCredentials = errors.New("NTLM proxy authentication requires username and password")
	errProxyAuthWithoutProxyURL        = errors.New("cannot specify proxy authentication without a proxy URL")
)

// applyCustomTransportSettings applies custom HTTP transport settings to the given transport.
// Only non-zero/non-default values are applied.
func applyCustomTransportSettings(transport *http.Transport, httpConfig config.HTTPConfig) {
	if httpConfig.IdleConnTimeout > 0 {
		transport.IdleConnTimeout = httpConfig.IdleConnTimeout
	}
	if httpConfig.MaxIdleConns > 0 {
		transport.MaxIdleConns = httpConfig.MaxIdleConns
	}
	if httpConfig.MaxIdleConnsPerHost > 0 {
		transport.MaxIdleConnsPerHost = httpConfig.MaxIdleConnsPerHost
	}
	if httpConfig.DisableKeepAlives {
		transport.DisableKeepAlives = true
	}
}

// formatTransportSettings returns a human-readable string of configured HTTP transport settings.
// Only non-zero/non-default values are included in the output.
func formatTransportSettings(httpConfig config.HTTPConfig) string {
	var settings []string
	if httpConfig.IdleConnTimeout > 0 {
		settings = append(settings, fmt.Sprintf("IdleConnTimeout=%s", httpConfig.IdleConnTimeout))
	}
	if httpConfig.MaxIdleConns > 0 {
		settings = append(settings, fmt.Sprintf("MaxIdleConns=%d", httpConfig.MaxIdleConns))
	}
	if httpConfig.MaxIdleConnsPerHost > 0 {
		settings = append(settings, fmt.Sprintf("MaxIdleConnsPerHost=%d", httpConfig.MaxIdleConnsPerHost))
	}
	if httpConfig.DisableKeepAlives {
		settings = append(settings, "DisableKeepAlives=true")
	}
	if len(settings) == 0 {
		return "none"
	}
	return strings.Join(settings, ", ")
}

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	config.ProxyConfig
	SDKHTTPConfigFactory *ldcomponents.HTTPConfigurationBuilder
	SDKHTTPConfig        subsystems.HTTPConfiguration
}

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig config.ProxyConfig, httpConfig config.HTTPConfig, authKey credential.SDKCredential, userAgent string, loggers ldlog.Loggers) (HTTPConfig, error) {
	configBuilder := ldcomponents.HTTPConfiguration()
	configBuilder.UserAgent(userAgent)

	ret := HTTPConfig{ProxyConfig: proxyConfig}

	authKeyStr := ""
	if authKey != nil {
		authKeyStr = authKey.GetAuthorizationHeaderValue()
	}

	if !proxyConfig.URL.IsDefined() && proxyConfig.NTLMAuth {
		return ret, errProxyAuthWithoutProxyURL
	}
	if proxyConfig.URL.IsDefined() {
		loggers.Infof("Using proxy server at %s", proxyConfig.URL.Get().Redacted())
	}

	caCertFiles := proxyConfig.CACertFiles.Values()

	// Build base transport options
	transportOpts := []ldhttp.TransportOption{
		ldhttp.ConnectTimeoutOption(ldcomponents.DefaultConnectTimeout),
	}

	// Add CA certificates if specified
	for _, filePath := range caCertFiles {
		if filePath != "" {
			transportOpts = append(transportOpts, ldhttp.CACertFileOption(filePath))
		}
	}

	// Check if custom HTTP transport settings are configured
	hasCustomTransportSettings := httpConfig.IdleConnTimeout > 0 ||
		httpConfig.MaxIdleConns > 0 ||
		httpConfig.MaxIdleConnsPerHost > 0 ||
		httpConfig.DisableKeepAlives

	if proxyConfig.NTLMAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errNTLMProxyAuthWithoutCredentials
		}
		factory, err := ldntlm.NewNTLMProxyHTTPClientFactory(proxyConfig.URL.String(),
			proxyConfig.User, proxyConfig.Password, proxyConfig.Domain, transportOpts...)
		if err != nil {
			return ret, err
		}

		// Wrap the NTLM factory to apply custom HTTP transport settings if configured
		if hasCustomTransportSettings {
			baseFactory := factory
			configBuilder.HTTPClientFactory(func() *http.Client {
				client := baseFactory()
				if transport, ok := client.Transport.(*http.Transport); ok {
					applyCustomTransportSettings(transport, httpConfig)
				} else {
					// This should never happen based on ldntlm implementation, but defend against it
					loggers.Warn("Unable to apply custom HTTP transport settings to NTLM proxy - unexpected transport type")
				}
				return client
			})
			loggers.Infof("NTLM proxy authentication enabled with custom HTTP transport (%s)",
				formatTransportSettings(httpConfig))
		} else {
			configBuilder.HTTPClientFactory(factory)
			loggers.Info("NTLM proxy authentication enabled")
		}
	} else {
		if proxyConfig.URL.IsDefined() {
			configBuilder.ProxyURL(proxyConfig.URL.String())
		}
		// Apply custom HTTP transport settings if specified
		if hasCustomTransportSettings || len(caCertFiles) > 0 {
			configBuilder.HTTPClientFactory(func() *http.Client {
				// Create base transport with cert files if needed
				transport, _, err := ldhttp.NewHTTPTransport(transportOpts...)
				if err != nil {
					// This should rarely happen, but log it if transport creation fails
					loggers.Warnf("Failed to create custom HTTP transport: %v - using default client", err)
					return &http.Client{}
				}

				// Apply custom transport settings
				applyCustomTransportSettings(transport, httpConfig)

				return &http.Client{Transport: transport}
			})

			// Log settings on initialization (not per client creation)
			if hasCustomTransportSettings {
				loggers.Infof("Custom HTTP transport configured: %s", formatTransportSettings(httpConfig))
			}
		}
	}

	var err error
	ret.SDKHTTPConfigFactory = configBuilder
	ret.SDKHTTPConfig, err = configBuilder.Build(subsystems.BasicClientContext{SDKKey: authKeyStr})
	return ret, err
}

// Client creates a new HTTP client instance that isn't for SDK use.
func (c HTTPConfig) Client() *http.Client {
	return c.SDKHTTPConfig.CreateHTTPClient()
}
