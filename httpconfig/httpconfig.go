package httpconfig

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	ntlm "github.com/Codehardt/go-ntlm-proxy-auth"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

// ProxyConfig represents all the supported proxy options. This is used in the Config struct in relay.go.
type ProxyConfig struct {
	Url      string
	UseNtlm  bool
	User     string
	Password string
	Domain   string
}

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	ProxyConfig
}

const defaultTimeout = 10 * time.Second

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig ProxyConfig) (HTTPConfig, error) {
	ret := HTTPConfig{proxyConfig}
	if proxyConfig.Url == "" && proxyConfig.UseNtlm {
		return ret, errors.New("Cannot specify proxy authentication without a proxy URL")
	}
	if proxyConfig.Url != "" {
		if _, err := url.Parse(proxyConfig.Url); err != nil {
			return ret, fmt.Errorf("Invalid proxy URL: %s", proxyConfig.Url)
		}
	}
	if proxyConfig.UseNtlm {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errors.New("NTLM proxy authentication requires username and password")
		}
	}
	return ret, nil
}

// Client creates a new HTTP client instance that isn't for SDK use.
func (c HTTPConfig) Client() *http.Client {
	client := c.newHTTPClient(defaultTimeout)
	return &client
}

// CreateHTTPClientForSDK creates an HTTP client for the Go SDK.
func (c HTTPConfig) CreateHTTPClientForSDK(config ld.Config) http.Client {
	return c.newHTTPClient(config.Timeout)
}

func (c HTTPConfig) newHTTPClient(timeout time.Duration) http.Client {
	var transport *http.Transport
	client := http.Client{}
	if c.ProxyConfig.UseNtlm {
		// See: https://github.com/Codehardt/go-ntlm-proxy-auth
		if transport == nil {
			transport = &http.Transport{}
		}
		dialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: timeout,
		}
		transport.Dial = dialer.Dial
		transport.DialContext = dialer.DialContext
		ntlmDialContext := ntlm.WrapDialContext(transport.DialContext, c.ProxyConfig.Url,
			c.ProxyConfig.User, c.ProxyConfig.Password, c.ProxyConfig.Domain)
		transport.DialContext = ntlmDialContext
	} else if c.ProxyConfig.Url != "" {
		if url, err := url.Parse(c.ProxyConfig.Url); err == nil {
			if transport == nil {
				transport = &http.Transport{}
			}
			transport.Proxy = http.ProxyURL(url)
		}
	}
	client.Transport = transport
	return client
}
