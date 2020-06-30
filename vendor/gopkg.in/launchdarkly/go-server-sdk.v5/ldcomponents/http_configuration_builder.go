package ldcomponents

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldhttp"
)

// DefaultConnectTimeout is the HTTP connection timeout that is used if HTTPConfigurationBuilder.ConnectTimeout
// is not set.
const DefaultConnectTimeout = 3 * time.Second

// HTTPConfigurationBuilder contains methods for configuring the SDK's networking behavior.
//
// If you want to set non-default values for any of these properties, create a builder with
// ldcomponents.HTTPConfiguration(), change its properties with the HTTPConfigurationBuilder methods,
// and store it in Config.HTTP:
//
//     config := ld.Config{
//         HTTP: ldcomponents.HTTPConfiguration().
//             ConnectTimeout(3 * time.Second).
//		       ProxyURL(proxyUrl),
//     }
type HTTPConfigurationBuilder struct {
	connectTimeout    time.Duration
	httpClientFactory func() *http.Client
	httpOptions       []ldhttp.TransportOption
	userAgent         string
	wrapperIdentifier string
}

// HTTPConfiguration returns a configuration builder for the SDK's HTTP configuration.
//
//     config := ld.Config{
//         HTTP: ldcomponents.HTTPConfiguration().
//             ConnectTimeout(3 * time.Second).
//		       ProxyURL(proxyUrl),
//     }
func HTTPConfiguration() *HTTPConfigurationBuilder {
	return &HTTPConfigurationBuilder{
		connectTimeout: DefaultConnectTimeout,
	}
}

// CACert specifies a CA certificate to be added to the trusted root CA list for HTTPS requests.
func (b *HTTPConfigurationBuilder) CACert(certData []byte) *HTTPConfigurationBuilder {
	b.httpOptions = append(b.httpOptions, ldhttp.CACertOption(certData))
	return b
}

// CACertFile specifies a CA certificate to be added to the trusted root CA list for HTTPS requests,
// reading the certificate data from a file in PEM format.
func (b *HTTPConfigurationBuilder) CACertFile(filePath string) *HTTPConfigurationBuilder {
	b.httpOptions = append(b.httpOptions, ldhttp.CACertFileOption(filePath))
	return b
}

// ConnectTimeout sets the connection timeout.
//
// This is the maximum amount of time to wait for each individual connection attempt to a remote service
// before determining that that attempt has failed. It is not the same as the timeout for initializing the
// SDK client (the waitFor parameter to MakeClient); that is the total length of time that MakeClient
// will wait regardless of how many connection attempts are required.
//
// This is equivalent to calling HTTPOptions(ldhttp.ConnectTimeoutOption(connectTimeout)).
//
//     config := ld.Config{
//         HTTP: ldcomponents.ConnectTimeout(),
//     }
func (b *HTTPConfigurationBuilder) ConnectTimeout(connectTimeout time.Duration) *HTTPConfigurationBuilder {
	if connectTimeout <= 0 {
		b.connectTimeout = DefaultConnectTimeout
	} else {
		b.connectTimeout = connectTimeout
	}
	return b
}

// HTTPClientFactory specifies a function for creating each HTTP client instance that is used by the SDK.
//
// If you use this option, it overrides any other settings that you may have specified with ConnectTimeout
// or HTTPOptions. The SDK may modify the client properties after the client is created (for instance, to
// add caching),  but will not replace the underlying Transport, and will not modify any timeout
// properties you set.
func (b *HTTPConfigurationBuilder) HTTPClientFactory(httpClientFactory func() *http.Client) *HTTPConfigurationBuilder {
	b.httpClientFactory = httpClientFactory
	return b
}

// ProxyURL specifies a proxy URL to be used for all requests. This overrides any setting of the
// HTTP_PROXY, HTTPS_PROXY, or NO_PROXY environment variables.
func (b *HTTPConfigurationBuilder) ProxyURL(proxyURL url.URL) *HTTPConfigurationBuilder {
	b.httpOptions = append(b.httpOptions, ldhttp.ProxyOption(proxyURL))
	return b
}

// UserAgent specifies an additional User-Agent header value to send with HTTP requests.
func (b *HTTPConfigurationBuilder) UserAgent(userAgent string) *HTTPConfigurationBuilder {
	b.userAgent = userAgent
	return b
}

// Wrapper allows wrapper libraries to set an identifying name for the wrapper being used.
//
// This will be sent in request headers during requests to the LaunchDarkly servers to allow recording
// metrics on the usage of these wrapper libraries.
func (b *HTTPConfigurationBuilder) Wrapper(wrapperName, wrapperVersion string) *HTTPConfigurationBuilder {
	if wrapperName == "" || wrapperVersion == "" {
		b.wrapperIdentifier = wrapperName
	} else {
		b.wrapperIdentifier = fmt.Sprintf("%s/%s", wrapperName, wrapperVersion)
	}
	return b
}

// DescribeConfiguration is internally by the SDK to inspect the configuration.
func (b *HTTPConfigurationBuilder) DescribeConfiguration() ldvalue.Value {
	builder := ldvalue.ObjectBuild()

	builder.Set("connectTimeoutMillis", durationToMillisValue(b.connectTimeout))
	builder.Set("socketTimeoutMillis", durationToMillisValue(b.connectTimeout))

	builder.Set("usingProxy", ldvalue.Bool(b.isProxyEnabled()))

	return builder.Build()
}

func (b *HTTPConfigurationBuilder) isProxyEnabled() bool {
	// There are several ways to implement an HTTP proxy in Go, not all of which we can detect from
	// here. We'll just report this as true if we reasonably suspect there is a proxy; the purpose
	// of this is just for general usage statistics.
	if os.Getenv("HTTP_PROXY") != "" {
		return true
	}
	if b.httpClientFactory != nil {
		return false // for a custom client configuration, we have no way to know how it works
	}
	for _, option := range b.httpOptions {
		if reflect.TypeOf(option) == reflect.TypeOf(ldhttp.ProxyOption(url.URL{})) {
			return true
		}
	}
	return false
}

// CreateHTTPConfiguration is called internally by the SDK.
func (b *HTTPConfigurationBuilder) CreateHTTPConfiguration(
	basicConfiguration interfaces.BasicConfiguration,
) (interfaces.HTTPConfiguration, error) {
	headers := make(http.Header)
	headers.Set("Authorization", basicConfiguration.SDKKey)
	userAgent := "GoClient/" + internal.SDKVersion
	if b.userAgent != "" {
		userAgent = userAgent + " " + b.userAgent
	}
	headers.Set("User-Agent", userAgent)
	if b.wrapperIdentifier != "" {
		headers.Add("X-LaunchDarkly-Wrapper", b.wrapperIdentifier)
	}

	clientFactory := b.httpClientFactory
	if clientFactory == nil {
		allOpts := []ldhttp.TransportOption{ldhttp.ConnectTimeoutOption(b.connectTimeout)}
		allOpts = append(allOpts, b.httpOptions...)
		transport, _, err := ldhttp.NewHTTPTransport(allOpts...)
		if err != nil {
			return nil, err
		}
		clientFactory = func() *http.Client {
			return &http.Client{
				Timeout:   b.connectTimeout,
				Transport: transport,
			}
		}
	}

	return internal.HTTPConfigurationImpl{
		DefaultHeaders:    headers,
		HTTPClientFactory: clientFactory,
	}, nil
}
