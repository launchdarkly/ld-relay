package httpconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	ntlm "github.com/Codehardt/go-ntlm-proxy-auth"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
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
	ProxyURL *url.URL
	CaCerts  *x509.CertPool
}

const defaultTimeout = 10 * time.Second

// NewHTTPConfig validates all of the HTTP-related options and returns an HTTPConfig if successful.
func NewHTTPConfig(proxyConfig ProxyConfig) (HTTPConfig, error) {
	ntlm.SetDebugf(log.Printf)
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
	if proxyConfig.NtlmAuth {
		if proxyConfig.User == "" || proxyConfig.Password == "" {
			return ret, errors.New("NTLM proxy authentication requires username and password")
		}
		logging.Info.Printf("NTLM proxy authentication enabled")
	}
	for _, filePath := range strings.Split(strings.TrimSpace(proxyConfig.CaCertFiles), ",") {
		bytes, err := ioutil.ReadFile(filePath)
		if err != nil {
			return ret, fmt.Errorf("Can't read CA certificate file %s", filePath)
		}
		if ret.CaCerts == nil {
			ret.CaCerts, err = x509.SystemCertPool() // this returns a *copy* of the existing CA certs
			if err != nil {
				ret.CaCerts = x509.NewCertPool()
			}
		}
		if !ret.CaCerts.AppendCertsFromPEM(bytes) {
			return ret, fmt.Errorf("CA certificate file %s did not contain a valid certificate", filePath)
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
	client := http.Client{}
	makeTLSConfig := func() *tls.Config {
		if c.CaCerts != nil {
			return &tls.Config{RootCAs: c.CaCerts}
		}
		return nil
	}
	makeProxyTransport := func() *http.Transport {
		return &http.Transport{TLSClientConfig: makeTLSConfig()}
	}
	if c.ProxyURL != nil {
		if c.ProxyConfig.NtlmAuth {
			// See: https://github.com/Codehardt/go-ntlm-proxy-auth
			transport := makeProxyTransport()
			dialer := &net.Dialer{
				Timeout:   timeout,
				KeepAlive: timeout,
			}
			transport.DialContext = ntlm.NewNTLMProxyDialContext(dialer, *c.ProxyURL,
				c.ProxyConfig.User, c.ProxyConfig.Password, c.ProxyConfig.Domain,
				makeTLSConfig())
			client.Transport = transport
		} else {
			transport := makeProxyTransport()
			transport.Proxy = http.ProxyURL(c.ProxyURL)
			client.Transport = transport
		}
	}
	return client
}
