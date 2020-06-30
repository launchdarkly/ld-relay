// Package ldntlm allows you to configure the SDK to connect to LaunchDarkly through a proxy server that
// uses NTLM authentication. The standard Go HTTP client proxy mechanism does not support this. The
// implementation uses this package: github.com/launchdarkly/go-ntlm-proxy-auth
//
// See NewNTLMProxyHTTPClientFactory for more details.
package ldntlm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	ntlm "github.com/launchdarkly/go-ntlm-proxy-auth"

	"gopkg.in/launchdarkly/go-server-sdk.v5/ldhttp"
)

// NewNTLMProxyHTTPClientFactory returns a factory function for creating an HTTP client that will
// connect through an NTLM-authenticated proxy server.
//
// To use this with the SDK, pass the factory function to HTTPConfigurationBuilder.HTTPClientFactory:
//
//     clientFactory, err := ldntlm.NewNTLMProxyHTTPClientFactory("http://my-proxy.com", "username",
//         "password", "domain")
//     if err != nil {
//         // there's some configuration problem such as an invalid proxy URL
//     }
//     config := ld.Config{
//         HTTP: ldcomponents.HTTPConfiguration().HTTPClientFactory(clientFactory),
//     }
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// You can also specify TLS configuration options from the ldhttp package, if you are connecting to
// the proxy securely:
//
//     clientFactory, err := ldntlm.NewNTLMProxyHTTPClientFactory("http://my-proxy.com", "username",
//         "password", "domain", ldhttp.CACertFileOption("extra-ca-cert.pem"))
func NewNTLMProxyHTTPClientFactory(proxyURL, username, password, domain string,
	options ...ldhttp.TransportOption) (func() *http.Client, error) {
	if proxyURL == "" || username == "" || password == "" {
		return nil, errors.New("ProxyURL, username, and password are required")
	}
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %s: %s", proxyURL, err)
	}
	// Try creating a transport with these options just to make sure it's valid before we get any farther
	if _, _, err := ldhttp.NewHTTPTransport(options...); err != nil {
		return nil, err
	}
	return func() *http.Client {
		client := *http.DefaultClient
		if transport, dialer, err := ldhttp.NewHTTPTransport(options...); err == nil {
			transport.DialContext = ntlm.NewNTLMProxyDialContext(dialer, *parsedProxyURL,
				username, password, domain, transport.TLSClientConfig)
			client.Transport = transport
		}
		return &client
	}, nil
}
