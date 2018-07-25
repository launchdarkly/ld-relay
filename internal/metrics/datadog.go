// +build go1.10

package metrics

import (
	datadog "github.com/DataDog/opencensus-go-exporter-datadog"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
)

func init() {
	defineExporter(datadogExporter, registerDatadogExporter)
}

func registerDatadogExporter(options ExporterOptions) error {
	o := options.(DatadogOptions)
	exporter := datadog.NewExporter(datadog.Options{Namespace: o.Prefix, Service: o.Prefix, TraceAddr: *o.TraceAddr, StatsAddr: *o.StatsAddr})
	view.RegisterExporter(exporter)
	trace.RegisterExporter(exporter)
	return nil
}
