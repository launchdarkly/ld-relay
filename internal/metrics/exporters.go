package metrics

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type ExporterType interface {
	getName() string
	createExporterIfEnabled(config.MetricsConfig, ldlog.Loggers) (Exporter, error)
}

type Exporter interface {
	register() error
	close() error
}

func allExporterTypes() []ExporterType {
	return []ExporterType{datadogExporterType, prometheusExporterType, stackdriverExporterType}
}

func registerExporters(
	exporterTypes []ExporterType,
	c config.MetricsConfig,
	loggers ldlog.Loggers,
) (map[ExporterType]Exporter, error) {
	registered := make(map[ExporterType]Exporter)
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

func closeExporters(exporters map[ExporterType]Exporter, loggers ldlog.Loggers) {
	for t, e := range exporters {
		if err := e.close(); err != nil {
			loggers.Errorf("Error closing %s metrics exporter: %s", t.getName(), err)
		}
	}
}

func getPrefix(c config.CommonMetricsConfig) string {
	if c.Prefix != "" {
		return c.Prefix
	}
	return defaultMetricsPrefix
}
