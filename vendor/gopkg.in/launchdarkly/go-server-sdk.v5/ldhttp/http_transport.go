// Package ldhttp provides internal helper functions for custom HTTP configuration.
//
// Applications will not normally need to use this package. Use the HTTP configuration options provided by
// ldcomponents.HTTPConfiguration() instead.
package ldhttp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"
)

const defaultConnectTimeout = 10 * time.Second

type transportExtraOptions struct {
	caCerts        *x509.CertPool
	connectTimeout time.Duration
	proxyURL       *url.URL
}

// TransportOption is the interface for optional configuration parameters that can be passed to NewHTTPTransport.
type TransportOption interface {
	apply(opts *transportExtraOptions) error
}

type connectTimeoutOption struct {
	timeout time.Duration
}

func (o connectTimeoutOption) apply(opts *transportExtraOptions) error {
	opts.connectTimeout = o.timeout
	return nil
}

// ConnectTimeoutOption specifies the maximum time to wait for a TCP connection, when used with
// NewHTTPTransport.
func ConnectTimeoutOption(timeout time.Duration) TransportOption {
	return connectTimeoutOption{timeout: timeout}
}

type caCertOption struct {
	certData []byte
}

func (o caCertOption) apply(opts *transportExtraOptions) error {
	if opts.caCerts == nil {
		var err error
		opts.caCerts, err = x509.SystemCertPool() // this returns a *copy* of the existing CA certs
		if err != nil {
			opts.caCerts = x509.NewCertPool() // COVERAGE: can't simulate this condition in unit tests
		}
	}
	if !opts.caCerts.AppendCertsFromPEM(o.certData) {
		return errors.New("invalid CA certificate data")
	}
	return nil
}

// CACertOption specifies a CA certificate to be added to the trusted root CA list for HTTPS requests,
// when used with NewHTTPTransport.
func CACertOption(certData []byte) TransportOption {
	return caCertOption{certData: certData}
}

type caCertFileOption struct {
	filePath string
}

func (o caCertFileOption) apply(opts *transportExtraOptions) error {
	bytes, err := ioutil.ReadFile(o.filePath)
	if err != nil {
		return fmt.Errorf("can't read CA certificate file: %v", err)
	}
	return caCertOption{certData: bytes}.apply(opts)
}

// CACertFileOption specifies a CA certificate to be added to the trusted root CA list for HTTPS requests,
// when used with NewHTTPTransport. It reads the certificate data from a file in PEM format.
func CACertFileOption(filePath string) TransportOption {
	return caCertFileOption{filePath: filePath}
}

// ProxyOption specifies a proxy URL to be used for all requests, when used with NewHTTPTransport.
// This overrides any setting of the HTTP_PROXY, HTTPS_PROXY, or NO_PROXY environment variables.
func ProxyOption(url url.URL) TransportOption {
	return proxyOption{url}
}

type proxyOption struct {
	url url.URL
}

func (o proxyOption) apply(opts *transportExtraOptions) error {
	opts.proxyURL = &o.url
	return nil
}

// NewHTTPTransport creates a customized http.Transport struct using the specified options. It returns both
// the Transport and an associated net.Dialer.
//
// To configure the LaunchDarkly SDK, rather than calling this function directly, it is simpler to use
// the methods provided by ldcomponents.HTTPConfiguration().
func NewHTTPTransport(options ...TransportOption) (*http.Transport, *net.Dialer, error) {
	extraOptions := transportExtraOptions{
		connectTimeout: defaultConnectTimeout,
	}
	for _, o := range options {
		err := o.apply(&extraOptions)
		if err != nil {
			return nil, nil, err
		}
	}
	dialer := &net.Dialer{
		Timeout:   extraOptions.connectTimeout,
		KeepAlive: 1 * time.Minute, // see newStreamProcessor for why we are setting this
	}
	transport := newDefaultTransport()
	transport.DialContext = dialer.DialContext
	if extraOptions.caCerts != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: extraOptions.caCerts}
	}
	if extraOptions.proxyURL != nil {
		transport.Proxy = http.ProxyURL(extraOptions.proxyURL)
	}
	return transport, dialer, nil
}

func newDefaultTransport() *http.Transport {
	// The reason we don't just make a copy of http.DefaultTransport is that Transport contains a
	// sync.Mutex, and copying a lock by value is bad.
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
