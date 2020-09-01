package metrics

import (
	"github.com/launchdarkly/ld-relay/v6/core/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// exporterType represents one of the kinds of OpenCensus exporters that we support.
type exporterType interface {
	// Returns the human-readable name, like "Datadog".
	getName() string

	// Checks the MetricsConfig and *if* this type of exporter is enabled in it, constructs an
	// implementation of the exporter interface containing the relevant configuration (but does not
	// register it yet). If this type of exporter is not enabled, returns (nil, nil).
	createExporterIfEnabled(config.MetricsConfig, ldlog.Loggers) (exporter, error)
}

type exporter interface {
	// Attempts to register this exporter with OpenCensus.
	register() error

	// Attempts to unregister this exporter with OpenCensus and release any other resources it uses.
	close() error
}

type exportersSet map[exporterType]exporter

func allExporterTypes() []exporterType {
	return []exporterType{datadogExporterType, prometheusExporterType, stackdriverExporterType}
}

// Attempts to create and register all of the types of exporters in exporterTypes that are actually
// enabled in the configuration. An error in any of them causes the whole operation to fail and
// unregisters any that have already been registered.
func registerExporters(
	exporterTypes []exporterType,
	c config.MetricsConfig,
	loggers ldlog.Loggers,
) (exportersSet, error) {
	registered := make(exportersSet)
	for _, t := range exporterTypes {
		exporter, err := t.createExporterIfEnabled(c, loggers)
		if err != nil {
			loggers.Errorf("Error creating %s metrics exporter: %s", t.getName(), err)
			closeExporters(registered, loggers)
			return nil, err
		}
		if exporter != nil {
			err := exporter.register()
			if err != nil {
				loggers.Errorf("Error registering %s metrics exporter: %s", t.getName(), err)
				closeExporters(registered, loggers)
				return nil, err
			}
			loggers.Infof("Successfully registered %s metrics exporter", t.getName())
			registered[t] = exporter
		}
	}
	return registered, nil
}

// Attempts to unregister and close a set of exporters. Errors are logged but do not stop the operation.
func closeExporters(exporters exportersSet, loggers ldlog.Loggers) {
	for t, e := range exporters {
		if err := e.close(); err != nil {
			loggers.Errorf("Error closing %s metrics exporter: %s", t.getName(), err)
		}
	}
}

func getPrefix(configuredPrefix string) string {
	if configuredPrefix != "" {
		return configuredPrefix
	}
	return defaultMetricsPrefix
}
