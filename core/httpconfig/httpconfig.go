package httpconfig

import (
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/internal/version"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldhttp"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldntlm"
)

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	config.ProxyConfig
	SDKHTTPConfigFactory interfaces.HTTPConfigurationFactory
	SDKHTTPConfig        interfaces.HTTPConfiguration
}

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig config.ProxyConfig, sdkKey config.SDKKey, loggers ldlog.Loggers) (HTTPConfig, error) {
	configBuilder := ldcomponents.HTTPConfiguration()
	configBuilder.UserAgent("LDRelay/" + version.Version)

	ret := HTTPConfig{ProxyConfig: proxyConfig}

	if !proxyConfig.URL.IsDefined() && proxyConfig.NTLMAuth {
		return ret, errors.New("Cannot specify proxy authentication without a proxy URL")
	}
	if proxyConfig.URL.IsDefined() {
		loggers.Infof("Using proxy server at %s", proxyConfig.URL)
	}

	caCertFiles := proxyConfig.CACertFiles.Values()

	if proxyConfig.NTLMAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errors.New("NTLM proxy authentication requires username and password")
		}
		transportOpts := []ldhttp.TransportOption{
			ldhttp.ConnectTimeoutOption(ldcomponents.DefaultConnectTimeout),
		}
		for _, filePath := range caCertFiles {
			if filePath != "" {
				transportOpts = append(transportOpts, ldhttp.CACertFileOption(filePath))
			}
		}
		factory, err := ldntlm.NewNTLMProxyHTTPClientFactory(proxyConfig.URL.String(),
			proxyConfig.User, proxyConfig.Password, proxyConfig.Domain, transportOpts...)
		if err != nil {
			return ret, err
		}
		configBuilder.HTTPClientFactory(factory)
		loggers.Info("NTLM proxy authentication enabled")
	} else {
		if proxyConfig.URL.IsDefined() {
			configBuilder.ProxyURL(proxyConfig.URL.String())
		}
		for _, filePath := range caCertFiles {
			if filePath != "" {
				configBuilder.CACertFile(filePath)
			}
		}
	}

	var err error
	ret.SDKHTTPConfigFactory = configBuilder
	ret.SDKHTTPConfig, err = configBuilder.CreateHTTPConfiguration(interfaces.BasicConfiguration{SDKKey: string(sdkKey)})
	return ret, err
}

// Client creates a new HTTP client instance that isn't for SDK use.
func (c HTTPConfig) Client() *http.Client {
	return c.SDKHTTPConfig.CreateHTTPClient()
}
