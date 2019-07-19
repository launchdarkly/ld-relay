package httpconfig

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldhttp"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldntlm"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

// ProxyConfig represents all the supported proxy options. This is used in the Config struct in relay.go.
type ProxyConfig struct {
	Url         string
	NtlmAuth    bool
	User        string
	Password    string
	Domain      string
	CaCertFiles string
}

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	ProxyConfig
	ProxyURL          *url.URL
	HTTPClientFactory ld.HTTPClientFactory
}

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig ProxyConfig) (HTTPConfig, error) {
	ret := HTTPConfig{ProxyConfig: proxyConfig}
	if proxyConfig.Url == "" && proxyConfig.NtlmAuth {
		return ret, errors.New("Cannot specify proxy authentication without a proxy URL")
	}
	if proxyConfig.Url != "" {
		u, err := url.Parse(proxyConfig.Url)
		if err != nil {
			return ret, fmt.Errorf("Invalid proxy URL: %s", proxyConfig.Url)
		}
		logging.Info.Printf("Using proxy server at %s", proxyConfig.Url)
		ret.ProxyURL = u
	}
	var transportOpts []ldhttp.TransportOption
	for _, filePath := range strings.Split(strings.TrimSpace(proxyConfig.CaCertFiles), ",") {
		if filePath != "" {
			transportOpts = append(transportOpts, ldhttp.CACertFileOption(filePath))
		}
	}
	if proxyConfig.NtlmAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errors.New("NTLM proxy authentication requires username and password")
		}
		var err error
		ret.HTTPClientFactory, err = ldntlm.NewNTLMProxyHTTPClientFactory(proxyConfig.Url,
			proxyConfig.User, proxyConfig.Password, proxyConfig.Domain, transportOpts...)
		if err != nil {
			return ret, err
		}
		logging.Info.Printf("NTLM proxy authentication enabled")
	} else {
		ret.HTTPClientFactory = ld.NewHTTPClientFactory(transportOpts...)
	}
	return ret, nil
}

// Client creates a new HTTP client instance that isn't for SDK use.
func (c HTTPConfig) Client() *http.Client {
	client := c.HTTPClientFactory(ld.DefaultConfig)
	return &client
}
