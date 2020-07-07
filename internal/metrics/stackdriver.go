package metrics

import (
	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/launchdarkly/ld-relay/v6/config"
)

var stackdriverExporterType ExporterType = stackdriverExporterTypeImpl{}

type stackdriverExporterTypeImpl struct{}

type stackdriverExporterImpl struct {
	exporter *stackdriver.Exporter
}

func (s stackdriverExporterTypeImpl) getName() string {
	return "Stackdriver"
}

func (s stackdriverExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (Exporter, error) {
	if !mc.Stackdriver.Enabled {
		return nil, nil
	}

	options := stackdriver.Options{
		MetricPrefix: getPrefix(mc.Stackdriver.CommonMetricsConfig),
		ProjectID:    mc.Stackdriver.ProjectID,
	}
	exporter, err := stackdriver.NewExporter(options)
	if err != nil {
		return nil, err
	}

	return &stackdriverExporterImpl{exporter: exporter}, nil
}

func (s *stackdriverExporterImpl) register() error {
	view.RegisterExporter(s.exporter)
	trace.RegisterExporter(s.exporter)
	return nil
}

func (s *stackdriverExporterImpl) close() error {
	view.UnregisterExporter(s.exporter)
	trace.UnregisterExporter(s.exporter)
	return nil
}
