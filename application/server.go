package application

import (
	"fmt"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// StartHTTPServer starts the server, with or without TLS. It returns immediately, starting the server
// on a separate goroutine; if the server fails to start up, it sends an error to the error channel.
func StartHTTPServer(
	mainConfig config.MainConfig,
	port int,
	handler http.Handler,
	loggers ldlog.Loggers,
) <-chan error {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
	}

	errCh := make(chan error)

	go func() {
		var err error
		loggers.Infof("Starting server listening on port %d\n", port)
		if mainConfig.TLSEnabled {
			loggers.Infof("TLS Enabled for server")
			err = srv.ListenAndServeTLS(mainConfig.TLSCert, mainConfig.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil {
			errCh <- err
		}
	}()

	return errCh
}
