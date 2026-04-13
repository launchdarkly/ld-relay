package main

import (
	"context"
	"log/slog"
	"os"

	_ "github.com/kardianos/minwinsvc"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/application"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
	"github.com/launchdarkly/ld-relay/v9/relay"
	"github.com/launchdarkly/ld-relay/v9/relay/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	var c config.Config

	// Create the initial logger with a shared LevelVar so the level can be
	// adjusted after config is loaded.
	levelVar := new(slog.LevelVar)
	logger := logging.NewLogger(logging.WithLevel(levelVar))

	opts, err := application.ReadOptions(os.Args, os.Stderr)
	if err != nil {
		logger.Error("error reading options", "error", err)
		return 1
	}

	if opts.PrintVersion {
		logger.Info("LaunchDarkly relay version",
			"version", application.DescribeRelayVersion(version.Version),
		)
		return 0
	}

	logger.Info("starting LaunchDarkly relay",
		"version", application.DescribeRelayVersion(version.Version),
		"configSource", opts.DescribeConfigSource(),
	)

	if opts.ConfigFile != "" {
		if err := config.LoadConfigFile(&c, opts.ConfigFile, logger); err != nil {
			logger.Error("error loading config file", "error", err)
			return 1
		}
	}
	if opts.UseEnvironment {
		if err := config.LoadConfigFromEnvironment(&c, logger); err != nil {
			logger.Error("configuration error", "error", err)
			return 1
		}
	}

	// Apply log level from configuration
	if c.Main.LogLevel.IsDefined() {
		levelVar.Set(c.Main.LogLevel.GetOrElse(slog.LevelInfo))
	}

	// If OTLP is enabled, recreate the logger with an OTel handler so that
	// log records are exported alongside metrics.
	if c.OpenTelemetry.Enabled {
		otelLog, err := logging.NewOTelLogProvider(logging.OTelLogConfig{
			Protocol: c.OpenTelemetry.Protocol,
		})
		if err != nil {
			logger.Error("failed to initialize OTLP log exporter", "error", err)
			return 1
		}
		defer otelLog.Shutdown(context.Background()) //nolint:errcheck

		logger = logging.NewLogger(
			logging.WithLevel(levelVar),
			logging.WithOTelHandler(otelLog.Handler),
		)
		logger.Info("OTLP log export enabled", "protocol", c.OpenTelemetry.Protocol)
	}

	r, err := relay.NewRelay(c, logger, nil)
	if err != nil {
		logger.Error("unable to create relay", "error", err)
		return 1
	}

	if c.Main.ExitAlways {
		return 0
	}

	port := c.Main.Port.GetOrElse(config.DefaultPort)

	_, errs := application.StartHTTPServer(
		port,
		r,
		c.Main.TLSEnabled,
		c.Main.TLSCert,
		c.Main.TLSKey,
		c.Main.TLSMinVersion.Get(),
		c.Main.GracefulShutdownTimeout.GetOrElse(config.DefaultGracefulShutdownTimeout),
		logger,
	)

	for err := range errs {
		logger.Error("error starting http listener", "port", port, "error", err)
		return 1
	}

	return 0
}
