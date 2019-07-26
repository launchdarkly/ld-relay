package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	_ "github.com/kardianos/minwinsvc"

	"gopkg.in/launchdarkly/ld-relay.v5"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/version"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "/etc/ld-relay.conf", "configuration file location")
	flag.Parse()

	logging.Info.Printf("Starting LaunchDarkly relay version %s with configuration file %s\n", formatVersion(version.Version), configFile)

	c := relay.DefaultConfig
	if err := relay.LoadConfigFile(&c, configFile); err != nil {
		log.Fatalf("Error loading config file: %s", err)
	}

	r, err := relay.NewRelay(c, relay.DefaultClientFactory)
	if err != nil {
		logging.Error.Printf("Unable to create relay: %s", err)
		os.Exit(1)
	}

	if err := relay.InitializeMetrics(c.MetricsConfig); err != nil {
		logging.Error.Printf("Error initializing metrics: %s", err)
	}

	errs := make(chan error)
	defer close(errs)

	startHTTPServer(&c, r, errs)

	for err := range errs {
		logging.Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err)
		os.Exit(1)
	}

}

func startHTTPServer(c *relay.Config, r *relay.Relay, errs chan<- error) {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", c.Main.Port),
		Handler: r,
	}

	go func() {
		var err error
		logging.Info.Printf("Starting server listening on port %d\n", c.Main.Port)
		if c.Main.TLSEnabled {
			logging.Info.Printf("TLS Enabled for server")
			err = srv.ListenAndServeTLS(c.Main.TLSCert, c.Main.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil {
			errs <- err
		}
	}()
}

func formatVersion(version string) string {
	split := strings.Split(version, "+")

	if len(split) == 2 {
		return fmt.Sprintf("%s (build %s)", split[0], split[1])
	}
	return version
}
