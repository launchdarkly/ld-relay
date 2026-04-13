package application

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/relay"
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
	logger *slog.Logger,
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

	errCh := make(chan error, 1)

	// Create a channel to listen for signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		var err error
		logger.Info("starting server", "port", port)
		if tlsEnabled {
			if tlsMinVersion != 0 {
				logger.Info("TLS enabled for server", "minTLSVersion", config.NewOptTLSVersion(tlsMinVersion).String())
			} else {
				logger.Info("TLS enabled for server")
			}
			err = srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Handle graceful shutdown in a separate goroutine
	go func() {
		<-sigCh
		logger.Info("received SIGTERM signal, initiating graceful shutdown")

		if relay, ok := handler.(*relay.Relay); ok {
			if err := relay.Close(); err != nil {
				logger.Error("error closing relay", "error", err)
			}
		}

		// Create a context with a timeout for graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("error during server shutdown", "error", err)
		} else {
			logger.Info("server gracefully stopped")
		}
	}()

	return srv, errCh
}
