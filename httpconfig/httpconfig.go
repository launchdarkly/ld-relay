package httpconfig

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	ntlm "github.com/launchdarkly/go-ntlm-proxy-auth"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

// ProxyConfig represents all the supported proxy options. This is used in the Config struct in relay.go.
type ProxyConfig struct {
	Url           string
	NtlmAuth      bool
	User          string
	Password      string
	Domain        string
	SkipTlsVerify bool
}

// HTTPConfig encapsulates ProxyConfig plus any other HTTP options we may support in the future (currently none).
type HTTPConfig struct {
	ProxyConfig
}

const defaultTimeout = 10 * time.Second

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig ProxyConfig) (HTTPConfig, error) {
	ntlm.SetDebugf(log.Printf)
	ret := HTTPConfig{proxyConfig}
	if proxyConfig.Url == "" && proxyConfig.NtlmAuth {
		return ret, errors.New("Cannot specify proxy authentication without a proxy URL")
	}
	if proxyConfig.Url != "" {
		if _, err := url.Parse(proxyConfig.Url); err != nil {
			return ret, fmt.Errorf("Invalid proxy URL: %s", proxyConfig.Url)
		}
		logging.Info.Printf("Using proxy server at %s", proxyConfig.Url)
	}
	if proxyConfig.NtlmAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errors.New("NTLM proxy authentication requires username and password")
		}
		logging.Info.Printf("NTLM proxy authentication enabled")
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
	client := http.Client{}
	var proxyURL *url.URL
	if c.ProxyConfig.Url != "" {
		proxyURL, _ = url.Parse(c.ProxyConfig.Url)
	}
	makeProxyTLSConfig := func() *tls.Config {
		if c.ProxyConfig.SkipTlsVerify {
			return &tls.Config{
				InsecureSkipVerify: true,
			}
		}
		return nil
	}
	makeProxyTransport := func() *http.Transport {
		return &http.Transport{TLSClientConfig: makeProxyTLSConfig()}
	}
	if c.ProxyConfig.NtlmAuth {
		// See: https://github.com/Codehardt/go-ntlm-proxy-auth
		transport := makeProxyTransport()
		dialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: timeout,
		}
		transport.DialContext = ntlm.NewNTLMProxyDialContext(dialer, *proxyURL,
			c.ProxyConfig.User, c.ProxyConfig.Password, c.ProxyConfig.Domain,
			makeProxyTLSConfig())
		client.Transport = transport
	} else if proxyURL != nil {
		transport := makeProxyTransport()
		transport.Proxy = http.ProxyURL(proxyURL)
		client.Transport = transport
	}
	return client
}
