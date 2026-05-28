package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/bridge/opencensus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const datadogDefaultOTLPEndpoint = "localhost:4317"

var datadogExporterType exporterType = datadogExporterTypeImpl{} //nolint:gochecknoglobals

type datadogExporterTypeImpl struct{}

type datadogExporterImpl struct {
	readers    []sdkmetric.Reader
	processors []sdktrace.SpanProcessor
	resource   *sdkresource.Resource
}

func (d datadogExporterTypeImpl) getName() string {
	return "Datadog"
}

// createExporterIfEnabled wires Datadog by exporting OTLP into a local Datadog Agent's OTLP
// receiver. This requires Datadog Agent v7.43+ with `otlp_config` enabled. The endpoint defaults
// to localhost:4317; the legacy DATADOG_TRACE_ADDR field is honored as an override for the gRPC
// host:port. DATADOG_STATS_ADDR (DogStatsD UDP) is no longer used and is logged as deprecated.
func (d datadogExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	if !mc.Datadog.Enabled {
		return nil, nil
	}

	if mc.Datadog.StatsAddr != "" {
		loggers.Warnf("DATADOG_STATS_ADDR (%q) is ignored: Relay now ships metrics to the Datadog Agent over OTLP. Configure the Agent's otlp_config receiver instead.", mc.Datadog.StatsAddr)
	}

	endpoint := strings.TrimSpace(mc.Datadog.TraceAddr)
	if endpoint == "" {
		endpoint = datadogDefaultOTLPEndpoint
	}
	endpoint = stripScheme(endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Datadog OTLP trace exporter for %s: %w", endpoint, err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Datadog OTLP metric exporter for %s: %w", endpoint, err)
	}

	reader := sdkmetric.NewPeriodicReader(metricExp,
		sdkmetric.WithProducer(opencensus.NewMetricProducer()),
	)

	return &datadogExporterImpl{
		readers:    []sdkmetric.Reader{reader},
		processors: []sdktrace.SpanProcessor{sdktrace.NewBatchSpanProcessor(traceExp)},
		resource:   datadogResourceAttributes(mc),
	}, nil
}

// datadogResourceAttributes converts the legacy DATADOG_TAG_* tag list and DATADOG_PREFIX into OTel
// resource attributes. The Datadog Agent's OTLP receiver maps these onto Datadog tags. The "name"
// or "service" tag is mapped to service.name when present.
func datadogResourceAttributes(mc config.MetricsConfig) *sdkresource.Resource {
	attrs := []attribute.KeyValue{}
	serviceName := getPrefix(mc.Datadog.Prefix)
	for _, raw := range mc.Datadog.Tag {
		k, v, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "service":
			serviceName = v
		default:
			attrs = append(attrs, attribute.String(k, v))
		}
	}
	attrs = append(attrs, semconv.ServiceName(serviceName))
	return sdkresource.NewSchemaless(attrs...)
}

func (d *datadogExporterImpl) metricReaders() []sdkmetric.Reader       { return d.readers }
func (d *datadogExporterImpl) spanProcessors() []sdktrace.SpanProcessor { return d.processors }
func (d *datadogExporterImpl) resourceAttributes() *sdkresource.Resource { return d.resource }
func (d *datadogExporterImpl) register(ldlog.Loggers) error            { return nil }
func (d *datadogExporterImpl) close() error                            { return nil }
