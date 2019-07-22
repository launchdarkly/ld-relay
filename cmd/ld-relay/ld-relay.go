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

	if c.Main.TLSEnabled {
		if c.Main.TLSCert == "" {
			logging.Error.Printf("TLS: tlsCert required")
			os.Exit(1)
		}
		if c.Main.TLSKey == "" {
			logging.Error.Printf("TLS: tlsKey required")
			os.Exit(1)
		}

		go func() {
			err = http.ListenAndServeTLS(fmt.Sprintf(":%d", c.Main.Port), c.Main.TLSCert, c.Main.TLSKey, r)
		}()
	} else {
		go func() {
			err = http.ListenAndServe(fmt.Sprintf(":%d", c.Main.Port), r)
		}()
	}

	if err != nil {
		if c.Main.ExitOnError {
			logging.Error.Fatalf("Error starting http listener on port: %d  %s", c.Main.Port, err)
		}
		logging.Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err)
	} else {
		if c.Main.TLSEnabled {
			logging.Info.Printf("TLS Enabled for Server")
		}
		logging.Info.Printf("Listening on port %d\n", c.Main.Port)
	}

	select {}
}

func formatVersion(version string) string {
	split := strings.Split(version, "+")

	if len(split) == 2 {
		return fmt.Sprintf("%s (build %s)", split[0], split[1])
	}
	return version
}
