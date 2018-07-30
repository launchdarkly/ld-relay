package metrics

import (
	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
)

func init() {
	defineExporter(stackdriverExporter, registerStackdriverExporter)
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
