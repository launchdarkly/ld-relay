package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	_ "github.com/kardianos/minwinsvc"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	relay "github.com/launchdarkly/ld-relay/v6"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/version"
)

const defaultConfigPath = "/etc/ld-relay.conf"

func main() {
	// The configuration parameter behavior is as follows:
	// 1. If you specify --config $FILEPATH, it loads that file. Failure to find it or parse it is a fatal error,
	//    unless you also specify --allow-missing-file.
	// 2. If you specify --from-env, it creates a configuration from environment variables as described in README.
	// 3. If you specify both, the file is loaded first, then it applies changes from variables if any.
	// 4. Omitting all options is equivalent to explicitly specifying --config /etc/ld-relay.conf.

	configFile := ""
	allowMissingFile := false
	useEnvironment := false
	flag.StringVar(&configFile, "config", "", "configuration file location")
	flag.BoolVar(&allowMissingFile, "allow-missing-file", false, "suppress error if config file is not found")
	flag.BoolVar(&useEnvironment, "from-env", false, "read configuration from environment variables")
	flag.Parse()

	c := config.DefaultConfig
	loggers := logging.MakeDefaultLoggers()

	if configFile == "" && !useEnvironment {
		configFile = defaultConfigPath
	}
	if configFile != "" && allowMissingFile {
		if _, err := os.Stat(configFile); err != nil && os.IsNotExist(err) {
			configFile = ""
		}
	}

	configDesc := ""
	if configFile != "" {
		configDesc = fmt.Sprintf("configuration file %s", configFile)
	}
	if useEnvironment {
		if configFile != "" {
			configDesc = configDesc + " plus environment variables"
		} else {
			configDesc = "configuration from environment variables"
		}
	}
	loggers.Infof("Starting LaunchDarkly relay version %s with %s\n", formatVersion(version.Version), configDesc)

	if configFile != "" {
		if err := config.LoadConfigFile(&c, configFile, loggers); err != nil {
			loggers.Errorf("Error loading config file: %s", err)
			os.Exit(1)
		}
	}
	if useEnvironment {
		if err := config.LoadConfigFromEnvironment(&c); err != nil {
			loggers.Errorf("Configuration error: %s", err)
			os.Exit(1)
		}
	}

	r, err := relay.NewRelay(c, loggers, relay.DefaultClientFactory)
	if err != nil {
		loggers.Errorf("Unable to create relay: %s", err)
		os.Exit(1)
	}

	if c.Main.ExitAlways {
		os.Exit(0)
	}

	if err := r.InitializeMetrics(); err != nil {
		loggers.Errorf("Error initializing metrics: %s", err)
	}

	errs := make(chan error)
	defer close(errs)

	startHTTPServer(&c, r, loggers, errs)

	for err := range errs {
		loggers.Errorf("Error starting http listener on port: %d  %s", c.Main.Port, err)
		os.Exit(1)
	}

}

func startHTTPServer(c *config.Config, r *relay.Relay, loggers ldlog.Loggers, errs chan<- error) {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", c.Main.Port),
		Handler: r,
	}

	go func() {
		var err error
		loggers.Infof("Starting server listening on port %d\n", c.Main.Port)
		if c.Main.TLSEnabled {
			loggers.Infof("TLS Enabled for server")
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
