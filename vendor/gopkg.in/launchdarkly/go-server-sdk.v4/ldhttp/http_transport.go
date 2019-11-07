// Package ldhttp provides helper functions for custom HTTP configuration. You will not need to use this package
// unless you need to extend the default Go HTTP client behavior, for instance, to specify additional trusted CA
// certificates.
package ldhttp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"time"
)

const defaultConnectTimeout = 10 * time.Second

type transportExtraOptions struct {
	caCerts        *x509.CertPool
	connectTimeout time.Duration
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
			opts.caCerts = x509.NewCertPool()
		}
	}
	if !opts.caCerts.AppendCertsFromPEM(o.certData) {
		return errors.New("Invalid CA certificate data")
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
		return fmt.Errorf("Can't read CA certificate file: %v", err)
	}
	return caCertOption{certData: bytes}.apply(opts)
}

// CACertFileOption specifies a CA certificate to be added to the trusted root CA list for HTTPS requests,
// when used with NewHTTPTransport. It reads the certificate data from a file in PEM format.
func CACertFileOption(filePath string) TransportOption {
	return caCertFileOption{filePath: filePath}
}

// NewHTTPTransport creates a customized http.Transport struct using the specified options. It returns both
// the Transport and an associated net.Dialer.
//
// To configure the LaunchDarkly SDK, rather than calling this function directly, it is simpler to use
// ld.NewHTTPClientFactory().
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
