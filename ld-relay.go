package main

import (
	"os"

	_ "github.com/kardianos/minwinsvc"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/application"
	"github.com/launchdarkly/ld-relay/v8/internal/logging"
	"github.com/launchdarkly/ld-relay/v8/relay"
	"github.com/launchdarkly/ld-relay/v8/relay/version"
)

func main() {
	var c config.Config
	loggers := logging.MakeDefaultLoggers()

	opts, err := application.ReadOptions(os.Args, os.Stderr)
	if err != nil {
		loggers.Errorf("Error: %s", err)
		os.Exit(1)
	}

	if opts.PrintVersion {
		loggers.Infof(
			"LaunchDarkly relay version %s\n",
			application.DescribeRelayVersion(version.Version),
		)
		os.Exit(0)
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
	host := c.Main.Host

	_, errs := application.StartHTTPServer(
		host,
		port,
		r,
		c.Main.TLSEnabled,
		c.Main.TLSCert,
		c.Main.TLSKey,
		c.Main.TLSMinVersion.Get(),
		loggers,
	)

	for err := range errs {
		loggers.Errorf("Error starting http listener on port: %d  %s", port, err)
		os.Exit(1)
	}
}
