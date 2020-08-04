package main

import (
	"os"

	_ "github.com/kardianos/minwinsvc"

	relay "github.com/launchdarkly/ld-relay/v6"
	"github.com/launchdarkly/ld-relay/v6/application"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/version"
)

func main() {
	var c config.Config
	loggers := logging.MakeDefaultLoggers()

	opts, err := application.ReadOptions(os.Args, os.Stderr)
	if err != nil {
		loggers.Errorf("Error: %s", err)
		os.Exit(1)
	}

	loggers.Infof(
		"Starting LaunchDarkly relay version %s with %s\n",
		application.DescribeRelayVersion(version.Version),
		opts.DescribeConfigSource(),
	)

	if opts.ConfigFile != "" {
		if err := config.LoadConfigFile(&c, opts.ConfigFile, loggers); err != nil {
			loggers.Errorf("Error loading config file: %s", err)
			os.Exit(1)
		}
	}
	if opts.UseEnvironment {
		if err := config.LoadConfigFromEnvironment(&c, loggers); err != nil {
			loggers.Errorf("Configuration error: %s", err)
			os.Exit(1)
		}
	}

	r, err := relay.NewRelay(c, loggers, nil)
	if err != nil {
		loggers.Errorf("Unable to create relay: %s", err)
		os.Exit(1)
	}

	if c.Main.ExitAlways {
		os.Exit(0)
	}

	port := c.Main.Port.GetOrElse(config.DefaultPort)

	errs := application.StartHTTPServer(c.Main, port, r, loggers)

	for err := range errs {
		loggers.Errorf("Error starting http listener on port: %d  %s", port, err)
		os.Exit(1)
	}
}
