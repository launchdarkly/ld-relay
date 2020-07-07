package metrics

import (
	"fmt"

	"go.opencensus.io/stats/view"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type ExporterType string

const (
	datadogExporterType     ExporterType = "Datadog"
	stackdriverExporterType ExporterType = "Stackdriver"
	prometheusExporterType  ExporterType = "Prometheus"
)

type ExporterOptions interface {
	getType() ExporterType
}

type ExporterRegisterer func(options ExporterOptions) error

// ExporterConfig is used internally to hold options for metrics integrations.
type ExporterConfig interface {
	toOptions() ExporterOptions
	enabled() bool
}

func ExporterOptionsFromConfig(c config.MetricsConfig) (options []ExporterOptions) {
	exporterConfigs := []ExporterConfig{
		DatadogConfig(c.Datadog),
		StackdriverConfig(c.Stackdriver),
		PrometheusConfig(c.Prometheus)}
	for _, e := range exporterConfigs {
		if e.enabled() {
			options = append(options, e.toOptions())
		}
	}
	return options
}

func getPrefix(c config.CommonMetricsConfig) string {
	if c.Prefix != "" {
		return c.Prefix
	}
	return defaultMetricsPrefix
}

func defineExporter(exporterType ExporterType, registerer ExporterRegisterer) {
	exporters[exporterType] = registerer
}

func RegisterExporters(options []ExporterOptions, loggers ldlog.Loggers) (registrationErr error) {
	registerPublicExportersOnce.Do(func() {
		for _, o := range options {
			exporter := exporters[o.getType()]
			if exporter == nil {
				registrationErr = fmt.Errorf("Got unexpected exporter type: %s", o.getType())
				return
			} else if err := exporter(o); err != nil {
				registrationErr = fmt.Errorf("Could not register %s exporter: %s", o.getType(), err)
				return
			} else {
				loggers.Infof("Successfully registered %s exporter.", o.getType())
			}
		}

		err := view.Register(getPublicViews()...)
		if err != nil {
			registrationErr = fmt.Errorf("Error registering metrics views")
		}
	})
	return registrationErr
}
