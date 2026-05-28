package metrics

import (
	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	gcpmetric "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric"
	gcptrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"go.opentelemetry.io/otel/bridge/opencensus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var stackdriverExporterType exporterType = stackdriverExporterTypeImpl{} //nolint:gochecknoglobals

type stackdriverExporterTypeImpl struct{}

type stackdriverExporterImpl struct {
	reader    sdkmetric.Reader
	processor sdktrace.SpanProcessor
}

func (s stackdriverExporterTypeImpl) getName() string {
	return "Stackdriver"
}

func (s stackdriverExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	_ ldlog.Loggers,
) (exporter, error) {
	if !mc.Stackdriver.Enabled {
		return nil, nil
	}

	metricExp, err := gcpmetric.New(gcpmetricOptions(mc)...)
	if err != nil {
		return nil, err
	}
	traceExp, err := gcptrace.New(gcptraceOptions(mc)...)
	if err != nil {
		return nil, err
	}

	// OC producer surfaces Relay's existing OpenCensus stats views as OTel metrics.
	reader := sdkmetric.NewPeriodicReader(metricExp,
		sdkmetric.WithProducer(opencensus.NewMetricProducer()),
	)

	return &stackdriverExporterImpl{
		reader:    reader,
		processor: sdktrace.NewBatchSpanProcessor(traceExp),
	}, nil
}

func gcpmetricOptions(mc config.MetricsConfig) []gcpmetric.Option {
	opts := []gcpmetric.Option{}
	if mc.Stackdriver.ProjectID != "" {
		opts = append(opts, gcpmetric.WithProjectID(mc.Stackdriver.ProjectID))
	}
	if prefix := getPrefix(mc.Stackdriver.Prefix); prefix != "" {
		opts = append(opts, gcpmetric.WithMetricDescriptorTypeFormatter(func(m metricdata.Metrics) string {
			return prefix + "/" + m.Name
		}))
	}
	return opts
}

func gcptraceOptions(mc config.MetricsConfig) []gcptrace.Option {
	opts := []gcptrace.Option{}
	if mc.Stackdriver.ProjectID != "" {
		opts = append(opts, gcptrace.WithProjectID(mc.Stackdriver.ProjectID))
	}
	return opts
}

func (s *stackdriverExporterImpl) metricReaders() []sdkmetric.Reader     { return []sdkmetric.Reader{s.reader} }
func (s *stackdriverExporterImpl) spanProcessors() []sdktrace.SpanProcessor { return []sdktrace.SpanProcessor{s.processor} }
func (s *stackdriverExporterImpl) resourceAttributes() *sdkresource.Resource { return nil }
func (s *stackdriverExporterImpl) register(ldlog.Loggers) error           { return nil }
func (s *stackdriverExporterImpl) close() error                           { return nil }
