package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

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

	srv := startHTTPServer(&c, r, errs)

	go startupCheck(&c, errs)

	for err := range errs {
		logging.Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err)
		if err := srv.Shutdown(context.TODO()); err != nil {
			logging.Error.Printf("Error shutting down HTTP Server: %s", err)
		}
		os.Exit(1)
	}

}

func startHTTPServer(c *relay.Config, r *relay.Relay, errs chan<- error) *http.Server {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", c.Main.Port),
		Handler: r,
	}

	go func() {
		if c.Main.TLSEnabled {
			logging.Info.Printf("TLS Enabled for Server")
			err := srv.ListenAndServeTLS(c.Main.TLSCert, c.Main.TLSKey)
			if err != nil {
				errs <- err
			}
		} else {
			err := srv.ListenAndServe()
			if err != nil {
				errs <- err
			}
		}
	}()

	return srv
}

func startupCheck(c *relay.Config, errs chan<- error) {
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		client := &http.Client{}
		scheme := "http"

		if c.Main.TLSEnabled {
			tr := &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // nolint: gas
			}
			client = &http.Client{Transport: tr}
			scheme = "https"
		}

		url := fmt.Sprintf("%s://:%d/status", scheme, c.Main.Port)
		resp, err := client.Get(url)
		if err != nil {
			logging.Warning.Println(err)
			continue
		}
		err = resp.Body.Close()
		if err != nil {
			logging.Warning.Println(err)
		}

		if resp.StatusCode != http.StatusOK {
			logging.Warning.Printf("HTTP Status Check failed: %d", resp.StatusCode)
			if i != 4 {
				continue
			}
		} else {
			logging.Info.Printf("Listening on port %d\n", c.Main.Port)
			break
		}

		if i == 4 {
			errs <- errors.New("getting server status failed")
		}
		break
	}
}

func formatVersion(version string) string {
	split := strings.Split(version, "+")

	if len(split) == 2 {
		return fmt.Sprintf("%s (build %s)", split[0], split[1])
	}
	return version
}
