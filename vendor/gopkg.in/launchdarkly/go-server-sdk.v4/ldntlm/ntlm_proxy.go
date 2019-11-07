// Package ldntlm allows you to configure the SDK to connect to LaunchDarkly through a proxy server that
// uses NTLM authentication. The standard Go HTTP client proxy mechanism does not support this. The
// implementation uses this package: github.com/launchdarkly/go-ntlm-proxy-auth
//
// Usage:
//
//     clientFactory, err := ldntlm.NewNTLMProxyHTTPClientFactory("http://my-proxy.com", "username",
//         "password", "domain")
//     if err != nil {
//         // there's some configuration problem such as an invalid proxy URL
//     }
//     config := ld.DefaultConfig
//     config.HTTPClientFactory = clientFactory
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// You can also specify TLS configuration options from the ldhttp package:
//
//     clientFactory, err := ldntlm.NewNTLMProxyHTTPClientFactory("http://my-proxy.com", "username",
//         "password", "domain", ldhttp.CACertFileOption("extra-ca-cert.pem"))
package ldntlm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	ntlm "github.com/launchdarkly/go-ntlm-proxy-auth"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldhttp"
)

// NewNTLMProxyHTTPClientFactory returns a factory function for creating an HTTP client that will
// connect through an NTLM-authenticated proxy server. This function should be placed in the
// HTTPClientFactory property of your Config object when you create the LaunchDarkly SDK client.
// If you are connecting to the proxy securely and need to specify any custom TLS options, these
// can be specified using the TransportOption values defined in the ldhttp package.
func NewNTLMProxyHTTPClientFactory(proxyURL, username, password, domain string,
	options ...ldhttp.TransportOption) (ld.HTTPClientFactory, error) {
	if proxyURL == "" || username == "" || password == "" {
		return nil, errors.New("ProxyURL, username, and password are required")
	}
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("Invalid proxy URL %s: %s", proxyURL, err)
	}
	// Try creating a transport with these options just to make sure it's valid before we get any farther
	if _, _, err := ldhttp.NewHTTPTransport(options...); err != nil {
		return nil, err
	}
	return func(config ld.Config) http.Client {
		client := *http.DefaultClient
		allOpts := []ldhttp.TransportOption{ldhttp.ConnectTimeoutOption(config.Timeout)}
		allOpts = append(allOpts, options...)
		if transport, dialer, err := ldhttp.NewHTTPTransport(allOpts...); err == nil {
			transport.DialContext = ntlm.NewNTLMProxyDialContext(dialer, *parsedProxyURL,
				username, password, domain, transport.TLSClientConfig)
			client.Transport = transport
		}
		return client
	}, nil
}
