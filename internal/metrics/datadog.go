// +build go1.10

package metrics

import (
	datadog "github.com/DataDog/opencensus-go-exporter-datadog"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v1/ldvalue"
)

func init() {
	defineExporter(datadogExporter, registerDatadogExporter)
}

type DatadogOptions struct {
	Prefix    string
	TraceAddr string
	StatsAddr string
	Tags      []string
}

func (d DatadogOptions) getType() ExporterType {
	return datadogExporter
}

type DatadogConfig config.DatadogConfig

func (c DatadogConfig) toOptions() ExporterOptions {
	// For historical reasons, TraceAddr and StatsAddr were declared as pointers in DatadogConfig. However,
	// if Datadog is enabled they must have non-nil values, so we have to change them to strings.
	return DatadogOptions{
		TraceAddr: ldvalue.NewOptionalStringFromPointer(c.TraceAddr).StringValue(),
		StatsAddr: ldvalue.NewOptionalStringFromPointer(c.StatsAddr).StringValue(),
		Tags:      c.Tag,
		Prefix:    getPrefix(c.CommonMetricsConfig),
	}
}

func (c DatadogConfig) enabled() bool {
	return c.Enabled
}

func registerDatadogExporter(options ExporterOptions) error {
	o := options.(DatadogOptions)
	exporter, err := datadog.NewExporter(datadog.Options{Namespace: o.Prefix, Service: o.Prefix, TraceAddr: o.TraceAddr, StatsAddr: o.StatsAddr, Tags: o.Tags})
	if err != nil {
		return err
	}
	view.RegisterExporter(exporter)
	trace.RegisterExporter(exporter)
	return nil
}
