package application

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/relay"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withSelfSignedCert(t *testing.T, action func(certFilePath, keyFilePath string, certPool *x509.CertPool)) {
	helpers.WithTempFile(func(certFilePath string) {
		helpers.WithTempFile(func(keyFilePath string) {
			err := httphelpers.MakeSelfSignedCert(certFilePath, keyFilePath)
			require.NoError(t, err)
			certData, err := os.ReadFile(certFilePath)
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
	port := st.GetAvailablePort(t)
	mockLog := ldlogtest.NewMockLog()
	server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK), false, "", "", 0, 30*time.Second, mockLog.Loggers)
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
	port := st.GetAvailablePort(t)
	mockLog := ldlogtest.NewMockLog()

	withSelfSignedCert(t, func(certFilePath, keyFilePath string, certPool *x509.CertPool) {
		server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK),
			true, certFilePath, keyFilePath, 0, 30*time.Second, mockLog.Loggers)
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
	port := st.GetAvailablePort(t)
	mockLog := ldlogtest.NewMockLog()

	withSelfSignedCert(t, func(certFilePath, keyFilePath string, certPool *x509.CertPool) {
		server, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(http.StatusOK),
			true, certFilePath, keyFilePath, tls.VersionTLS12, 30*time.Second, mockLog.Loggers)
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
			// the exact error message varies by Go version
			return strings.Contains(err.Error(), "protocol version not supported") ||
				strings.Contains(err.Error(), "tls: no supported versions")
		}, time.Second, time.Millisecond*10)
		mockLog.AssertMessageMatch(t, true, ldlog.Info, fmt.Sprintf("listening on port %d", port))
		mockLog.AssertMessageMatch(t, true, ldlog.Info, "TLS enabled for server \\(minimum TLS version: 1.2\\)")
	})
}

func TestStartHTTPServerPortAlreadyUsed(t *testing.T) {
	st.WithListenerForAnyPort(t, func(l net.Listener, port int) {
		_, errCh := StartHTTPServer(port, httphelpers.HandlerWithStatus(200), false, "", "", 0, 30*time.Second, ldlog.NewDisabledLoggers())
		require.NotNil(t, errCh)
		err := helpers.RequireValue(t, errCh, time.Second, "timed out waiting for error")
		assert.NotNil(t, err)
	})
}

func TestStartHTTPServerGracefulShutdown(t *testing.T) {
	port := st.GetAvailablePort(t)
	mockLog := ldlogtest.NewMockLog()

	// Create a handler that takes some time to complete
	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	server, errCh := StartHTTPServer(port, slowHandler, false, "", "", 0, 30*time.Second, mockLog.Loggers)
	require.NotNil(t, server)
	require.NotNil(t, errCh)

	// Wait for server to start and verify initial log message
	require.Eventually(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
		if err != nil {
			return false
		}
		return resp.StatusCode == http.StatusOK
	}, time.Second, time.Millisecond*10)
	mockLog.AssertMessageMatch(t, true, ldlog.Info, fmt.Sprintf(`Starting server listening on port %d`, port))

	// Start a long-running request in the background
	requestDone := make(chan struct{})
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		close(requestDone)
	}()

	// Give the request a moment to start
	time.Sleep(10 * time.Millisecond)

	// Send SIGTERM signal
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, process.Signal(syscall.SIGTERM))

	// Wait for the request to complete
	select {
	case <-requestDone:
		// Request completed successfully
	case <-time.After(1000 * time.Millisecond):
		t.Fatal("Request did not complete within timeout")
	}

	// Wait for server to fully shut down
	require.Eventually(t, func() bool {
		_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
		return err != nil
	}, 20*time.Second, time.Millisecond*10)

	// Give a moment for the final log message to be written
	time.Sleep(100 * time.Millisecond)

	// Verify shutdown messages were logged
	mockLog.AssertMessageMatch(t, true, ldlog.Info, `Received SIGTERM signal, initiating graceful shutdown\.\.\.`)
	mockLog.AssertMessageMatch(t, true, ldlog.Info, `Server gracefully stopped`)

	// Verify no errors were sent to error channel
	select {
	case err, ok := <-errCh:
		if ok {
			t.Fatalf("Unexpected error from server: %v", err)
		}
		// Channel was closed, which is expected
	default:
		t.Fatal("Error channel was not closed")
	}
}

func TestStartHTTPServerWithRelayHandler(t *testing.T) {
	port := st.GetAvailablePort(t)
	mockLog := ldlogtest.NewMockLog()
	urlStr := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Create a basic relay configuration
	parsedURL, err := url.Parse(urlStr)
	require.NoError(t, err)
	streamURI, err := ct.NewOptURLAbsolute(parsedURL)
	require.NoError(t, err)

	relayConfig := config.Config{
		Main: config.MainConfig{
			StreamURI: streamURI,
		},
		AutoConfig: config.AutoConfigConfig{
			Key: "x",
		},
	}
	r, err := relay.NewRelay(relayConfig, mockLog.Loggers, nil)
	require.NoError(t, err)

	// Start the server with the relay handler
	server, errCh := StartHTTPServer(
		port,
		r,
		false, // No TLS
		"",    // No cert file
		"",    // No key file
		0,     // No min TLS version
		1*time.Second,
		mockLog.Loggers,
	)
	require.NotNil(t, server)
	require.NotNil(t, errCh)

	// Send SIGTERM signal
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, process.Signal(syscall.SIGTERM))

	// Wait for server to fully shut down
	require.Eventually(t, func() bool {
		_, err := http.Get(urlStr)
		return err != nil
	}, 20*time.Second, time.Millisecond*10)

	// Give a moment for the final log message to be written
	time.Sleep(100 * time.Millisecond)

	// Verify shutdown messages were logged
	mockLog.AssertMessageMatch(t, true, ldlog.Info, `Received SIGTERM signal, initiating graceful shutdown\.\.\.`)
	mockLog.AssertMessageMatch(t, true, ldlog.Info, `Server gracefully stopped`)
	mockLog.AssertMessageMatch(t, true, ldlog.Info, `Shutting down Relay Proxy`)

	// Verify no errors were sent to error channel
	select {
	case err, ok := <-errCh:
		if ok {
			t.Fatalf("Unexpected error from server: %v", err)
		}
		// Channel was closed, which is expected
	default:
		t.Fatal("Error channel was not closed")
	}
}
