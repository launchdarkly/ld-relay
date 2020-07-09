package metrics

import (
	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"

	"github.com/launchdarkly/ld-relay/v6/config"
)

func init() {
	defineExporter(stackdriverExporterType, registerStackdriverExporter)
}

type StackdriverOptions struct {
	Prefix    string
	ProjectID string
}

func (d StackdriverOptions) getType() ExporterType {
	return stackdriverExporterType
}

type StackdriverConfig config.StackdriverConfig

func (c StackdriverConfig) toOptions() ExporterOptions {
	return StackdriverOptions{
		ProjectID: c.ProjectID,
		Prefix:    getPrefix(c.CommonMetricsConfig),
	}
}

func (c StackdriverConfig) enabled() bool {
	return c.Enabled
}

func registerStackdriverExporter(options ExporterOptions) error {
	o := options.(StackdriverOptions)
	exporter, err := stackdriver.NewExporter(stackdriver.Options{
		MetricPrefix: o.Prefix,
		ProjectID:    o.ProjectID,
	})
	if err != nil {
		return err
	}
	view.RegisterExporter(exporter)
	trace.RegisterExporter(exporter)
	return nil
}
