package metrics

import (
	datadog "github.com/DataDog/opencensus-go-exporter-datadog"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"

	config "github.com/launchdarkly/ld-relay-config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var datadogExporterType exporterType = datadogExporterTypeImpl{} //nolint:gochecknoglobals

type datadogExporterTypeImpl struct{}

type datadogExporterImpl struct {
	exporter *datadog.Exporter
}

func (d datadogExporterTypeImpl) getName() string {
	return "Datadog"
}

func (d datadogExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	if !mc.Datadog.Enabled {
		return nil, nil
	}

	options := datadog.Options{
		Namespace: getPrefix(mc.Datadog.Prefix),
		Service:   getPrefix(mc.Datadog.Prefix),
		TraceAddr: mc.Datadog.TraceAddr,
		StatsAddr: mc.Datadog.StatsAddr,
		Tags:      mc.Datadog.Tag,
	}
	exporter, err := datadog.NewExporter(options)
	if err != nil {
		return nil, err
	}
	return &datadogExporterImpl{exporter: exporter}, nil
}

func (d *datadogExporterImpl) register() error {
	view.RegisterExporter(d.exporter)
	trace.RegisterExporter(d.exporter)
	return nil
}

func (d *datadogExporterImpl) close() error {
	d.exporter.Stop()
	view.UnregisterExporter(d.exporter)
	trace.UnregisterExporter(d.exporter)
	return nil
}
