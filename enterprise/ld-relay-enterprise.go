package enterprise

import (
	"os"

	_ "github.com/kardianos/minwinsvc"

	"github.com/launchdarkly/ld-relay/v6/core/application"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entrelay"
	"github.com/launchdarkly/ld-relay/v6/internal/version"
)

func main() {
	var c entconfig.EnterpriseConfig
	loggers := logging.MakeDefaultLoggers()

	opts, err := application.ReadOptions(os.Args, os.Stderr)
	if err != nil {
		loggers.Errorf("Error: %s", err)
		os.Exit(1)
	}

	loggers.Infof(
		"Starting LaunchDarkly Relay Proxy Enterprise version %s with %s\n",
		application.DescribeRelayVersion(version.Version),
		opts.DescribeConfigSource(),
	)

	if opts.ConfigFile != "" {
		if err := entconfig.LoadConfigFile(&c, opts.ConfigFile, loggers); err != nil {
			loggers.Errorf("Error loading config file: %s", err)
			os.Exit(1)
		}
	}
	if opts.UseEnvironment {
		if err := entconfig.LoadConfigFromEnvironment(&c, loggers); err != nil {
			loggers.Errorf("Configuration error: %s", err)
			os.Exit(1)
		}
	}

	r, err := entrelay.NewRelayEnterprise(c, loggers, nil)
	if err != nil {
		loggers.Errorf("Unable to create relay: %s", err)
		os.Exit(1)
	}

	if c.Main.ExitAlways {
		os.Exit(0)
	}

	port := c.Main.Port.GetOrElse(config.DefaultPort)

	errs := application.StartHTTPServer(
		port,
		r.GetHandler(),
		c.Main.TLSEnabled,
		c.Main.TLSCert,
		c.Main.TLSKey,
		loggers,
	)

	for err := range errs {
		loggers.Errorf("Error starting HTTP listener on port: %d  %s", port, err)
		os.Exit(1)
	}
}
