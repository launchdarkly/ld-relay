package application

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	helpers "github.com/launchdarkly/go-test-helpers/v2"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startListenerForAnyAvailablePort(t *testing.T) (net.Listener, int) {
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	addr := l.Addr().String()
	port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	require.NoError(t, err)
	return l, port
}

func withSelfSignedCert(t *testing.T, action func(certFilePath, keyFilePath string, certPool *x509.CertPool)) {
	helpers.WithTempFile(func(certFilePath string) {
		helpers.WithTempFile(func(keyFilePath string) {
			err := httphelpers.MakeSelfSignedCert(certFilePath, keyFilePath)
			require.NoError(t, err)
			certData, err := ioutil.ReadFile(certFilePath)
			require.NoError(t, err)
			certPool, err := x509.SystemCertPool()
			if err != nil {
				certPool = x509.NewCertPool()
			}
			certPool.AppendCertsFromPEM(certData)

			action(certFilePath, keyFilePath, certPool)
		})
	})
}

func TestStartHTTPServerInsecure(t *testing.T) {
	l, port := startListenerForAnyAvailablePort(t)
	l.Close()
	mockLog := ldlogtest.NewMockLog()
	server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK), false, "", "", 0, mockLog.Loggers)
	require.NotNil(t, server)
	require.NotNil(t, errCh)
	require.Eventually(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d", port))
		require.NoError(t, err)
		return resp.StatusCode == http.StatusOK
	}, time.Second, time.Millisecond*10)
	mockLog.AssertMessageMatch(t, true, ldlog.Info, fmt.Sprintf("listening on port %d", port))
	mockLog.AssertMessageMatch(t, false, ldlog.Info, "TLS enabled")
}

func TestStartHTTPServerSecure(t *testing.T) {
	l, port := startListenerForAnyAvailablePort(t)
	l.Close()
	mockLog := ldlogtest.NewMockLog()

	withSelfSignedCert(t, func(certFilePath, keyFilePath string, certPool *x509.CertPool) {
		server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK),
			true, certFilePath, keyFilePath, 0, mockLog.Loggers)
		require.NotNil(t, server)
		require.NotNil(t, errCh)

		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		}}

		require.Eventually(t, func() bool {
			resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d", port))
			require.NoError(t, err)
			return resp.StatusCode == http.StatusOK
		}, time.Second, time.Millisecond*10)
		mockLog.AssertMessageMatch(t, true, ldlog.Info, fmt.Sprintf("listening on port %d", port))
		mockLog.AssertMessageMatch(t, true, ldlog.Info, "TLS enabled for server")
	})
}

func TestStartHTTPServerSecureWithMinTLSVersion(t *testing.T) {
	l, port := startListenerForAnyAvailablePort(t)
	l.Close()
	mockLog := ldlogtest.NewMockLog()

	withSelfSignedCert(t, func(certFilePath, keyFilePath string, certPool *x509.CertPool) {
		server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK),
			true, certFilePath, keyFilePath, tls.VersionTLS12, mockLog.Loggers)
		require.NotNil(t, server)
		require.NotNil(t, errCh)

		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    certPool,
				MaxVersion: tls.VersionTLS11,
			},
		}}

		require.Eventually(t, func() bool {
			_, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d", port))
			require.Error(t, err)
			return strings.Contains(err.Error(), "protocol version not supported")
		}, time.Second, time.Millisecond*10)
		mockLog.AssertMessageMatch(t, true, ldlog.Info, fmt.Sprintf("listening on port %d", port))
		mockLog.AssertMessageMatch(t, true, ldlog.Info, "TLS enabled for server \\(minimum TLS version: 1.2\\)")
	})
}

func TestStartHTTPServerPortAlreadyUsed(t *testing.T) {
	l, port := startListenerForAnyAvailablePort(t)
	defer l.Close()
	_, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(200), false, "", "", 0, ldlog.NewDisabledLoggers())
	require.NotNil(t, errCh)
	select {
	case err := <-errCh:
		assert.NotNil(t, err)
	case <-time.After(time.Second):
		assert.Fail(t, "timed out waiting for error")
	}
}