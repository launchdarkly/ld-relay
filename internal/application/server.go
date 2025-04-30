package application

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

// StartHTTPServer starts the server, with or without TLS. It returns immediately, starting the server
// on a separate goroutine; if the server fails to start up, it sends an error to the error channel.
func StartHTTPServer(
	port int,
	handler http.Handler,
	tlsEnabled bool,
	tlsCertFile, tlsKeyFile string,
	tlsMinVersion uint16,
	gracefulShutdownTimeout time.Duration,
	loggers ldlog.Loggers,
) (*http.Server, <-chan error) {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if tlsEnabled && tlsMinVersion != 0 {
		srv.TLSConfig = &tls.Config{ //nolint:gosec // linter doesn't want to see MinVersion being set to a variable
			MinVersion: tlsMinVersion,
		}
	}

	errCh := make(chan error)

	// Create a channel to listen for signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		var err error
		loggers.Infof("Starting server listening on port %d\n", port)
		if tlsEnabled {
			message := "TLS enabled for server"
			if tlsMinVersion != 0 {
				message += fmt.Sprintf(" (minimum TLS version: %s)", config.NewOptTLSVersion(tlsMinVersion).String())
			}
			loggers.Info(message)
			err = srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Handle graceful shutdown in a separate goroutine
	go func() {
		<-sigCh
		loggers.Info("Received SIGTERM signal, initiating graceful shutdown...")

		// Create a context with a timeout for graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			loggers.Errorf("Error during server shutdown: %v", err)
		} else {
			loggers.Info("Server gracefully stopped")
		}
		close(errCh) // Close the error channel after shutdown
	}()

	return srv, errCh
}
