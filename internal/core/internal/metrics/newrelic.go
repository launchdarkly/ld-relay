package metrics

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	newrelic "github.com/newrelic/newrelic-opencensus-exporter-go/nrcensus"
	"github.com/newrelic/newrelic-telemetry-sdk-go/telemetry"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var newrelicExporterType exporterType = newrelicExporterTypeImpl{} //nolint:gochecknoglobals

type newrelicExporterTypeImpl struct{}

type newrelicExporterImpl struct {
	exporter *newrelic.Exporter
}

func (nr newrelicExporterTypeImpl) getName() string {
	return "Newrelic"
}

func (nr newrelicExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {

	if !mc.Newrelic.Enabled {
		return nil, nil
	}

	appName := mc.Newrelic.AppName
	if len(appName) == 0 {
		appName = "ld-relay"
	}
	commonAttributes := map[string]interface{}{
		"prefix":  mc.Newrelic.Prefix,
		"relayID": appName,
	}

	exporter, err := newrelic.NewExporter(appName,
		mc.Newrelic.InsightsKey,
		telemetry.ConfigCommonAttributes(commonAttributes),
		telemetry.ConfigMetricsURLOverride(mc.Newrelic.MetricsURL),
		telemetry.ConfigSpansURLOverride(mc.Newrelic.TraceURL),
		telemetry.ConfigEventsURLOverride(mc.Newrelic.EventsURL),
	)

	if err != nil {
		return nil, err
	}
	return &newrelicExporterImpl{exporter: exporter}, nil
}

func (nr *newrelicExporterImpl) register() error {
	view.RegisterExporter(nr.exporter)
	trace.RegisterExporter(nr.exporter)
	return nil
}

func (nr *newrelicExporterImpl) close() error {
	view.UnregisterExporter(nr.exporter)
	trace.UnregisterExporter(nr.exporter)
	return nil
}
