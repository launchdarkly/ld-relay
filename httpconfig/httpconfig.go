package httpconfig

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	ntlm "github.com/Codehardt/go-ntlm-proxy-auth"
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

// Client creates a new HTTP client instance based on the configuration.
func (c HTTPConfig) Client() *http.Client {
	client := c.TransformHTTPClient(http.Client{})
	return &client
}

// TransformHTTPClient modifies an existing HTTP client instance. Having this method allows us to
// use HTTPConfig as a adapter for the Go SDK.
func (c HTTPConfig) TransformHTTPClient(client http.Client) http.Client {
	if c.ProxyConfig.UseNtlm {
		// See: https://github.com/Codehardt/go-ntlm-proxy-auth
		transport, _ := client.Transport.(*http.Transport)
		if transport == nil {
			transport = &http.Transport{}
			client.Transport = transport
		} else {
			// copy the existing Transport object
			t := *transport
			transport = &t
		}
		if transport.DialContext == nil {
			dialer := &net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}
			transport.Dial = dialer.Dial
			transport.DialContext = dialer.DialContext
		}
		ntlmDialContext := ntlm.WrapDialContext(transport.DialContext, c.ProxyConfig.Url,
			c.ProxyConfig.User, c.ProxyConfig.Password, c.ProxyConfig.Domain)
		transport.DialContext = ntlmDialContext
	}
	if c.ProxyConfig.Url != "" {
		if url, err := url.Parse(c.ProxyConfig.Url); err == nil {
			transport, _ := client.Transport.(*http.Transport)
			if transport == nil {
				transport = &http.Transport{}
				client.Transport = transport
			}
			transport.Proxy = http.ProxyURL(url)
		}
	}
	return client
}
