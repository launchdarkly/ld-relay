// Package httpconfig provides helpers for special types of HTTP client configuration supported by Relay.
package httpconfig

import (
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/ldhttp"
	"github.com/launchdarkly/go-server-sdk/v7/ldntlm"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

var (
	errNTLMProxyAuthWithoutCredentials = errors.New("NTLM proxy authentication requires username and password")
	errProxyAuthWithoutProxyURL        = errors.New("cannot specify proxy authentication without a proxy URL")
)

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	config.ProxyConfig
	SDKHTTPConfigFactory *ldcomponents.HTTPConfigurationBuilder
	SDKHTTPConfig        subsystems.HTTPConfiguration
}

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig config.ProxyConfig, httpConfig config.HTTPConfig, authKey credential.SDKCredential, userAgent string, loggers ldlog.Loggers) (HTTPConfig, error) {
	transportOpts := []ldhttp.TransportOption{
		ldhttp.ConnectTimeoutOption(ldcomponents.DefaultConnectTimeout),
		ldhttp.MaxIdleConnsPerHostOption(httpConfig.MaxIdleConnsPerHost),
		ldhttp.DisableKeepAlivesOption(httpConfig.DisableKeepAlives),
	}

	if httpConfig.IdleConnTimeout.IsDefined() {
		transportOpts = append(transportOpts, ldhttp.IdleConnTimeoutOption(httpConfig.IdleConnTimeout.GetOrElse(0)))
	}

	if httpConfig.MaxIdleConns.IsDefined() {
		transportOpts = append(transportOpts, ldhttp.MaxIdleConnsOption(httpConfig.MaxIdleConns.GetOrElse(0)))
	}

	caCertFiles := proxyConfig.CACertFiles.Values()

	// Add CA certificates if specified
	for _, filePath := range caCertFiles {
		if filePath != "" {
			transportOpts = append(transportOpts, ldhttp.CACertFileOption(filePath))
		}
	}

	configBuilder := ldcomponents.HTTPConfiguration()
	configBuilder.UserAgent(userAgent)
	configBuilder.HTTPOptions(transportOpts)

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

	if proxyConfig.NTLMAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errNTLMProxyAuthWithoutCredentials
		}

		factory, err := ldntlm.NewNTLMProxyHTTPClientFactory(proxyConfig.URL.String(),
			proxyConfig.User, proxyConfig.Password, proxyConfig.Domain, transportOpts...)
		if err != nil {
			return ret, err
		}

		configBuilder.HTTPClientFactory(factory)
		loggers.Info("NTLM proxy authentication enabled")
	} else if proxyConfig.URL.IsDefined() {
		configBuilder.ProxyURL(proxyConfig.URL.String())
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
