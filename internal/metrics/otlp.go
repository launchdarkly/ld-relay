package metrics

import (
	"context"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/bridge/opencensus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var otlpExporterType exporterType = otlpExporterTypeImpl{} //nolint:gochecknoglobals

type otlpExporterTypeImpl struct{}

type otlpExporterImpl struct {
	traceExporter  *otlptrace.Exporter
	metricExporter sdkmetric.Exporter
	resource       *resource.Resource
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

func (o otlpExporterTypeImpl) getName() string {
	return "OTLP"
}

func (o otlpExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	if !mc.OTLP.Enabled {
		return nil, nil
	}

	ctx := context.Background()
	headers := parseOTLPHeaders(mc.OTLP.Headers)

	traceOptions := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(mc.OTLP.Endpoint)}
	metricOptions := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(mc.OTLP.Endpoint)}
	if mc.OTLP.Insecure {
		traceOptions = append(traceOptions, otlptracegrpc.WithInsecure())
		metricOptions = append(metricOptions, otlpmetricgrpc.WithInsecure())
	}
	if len(headers) > 0 {
		traceOptions = append(traceOptions, otlptracegrpc.WithHeaders(headers))
		metricOptions = append(metricOptions, otlpmetricgrpc.WithHeaders(headers))
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceOptions...)
	if err != nil {
		return nil, err // COVERAGE: can't make this happen in unit tests
	}
	metricExporter, err := otlpmetricgrpc.New(ctx, metricOptions...)
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, err // COVERAGE: can't make this happen in unit tests
	}

	return &otlpExporterImpl{
		traceExporter:  traceExporter,
		metricExporter: metricExporter,
		resource:       resource.NewSchemaless(attribute.String("service.name", getPrefix(mc.OTLP.Prefix))),
	}, nil
}

func (o *otlpExporterImpl) register() error {
	o.tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(o.traceExporter),
		sdktrace.WithResource(o.resource),
	)
	// Bridge Relay's OpenCensus route-trace spans into the OTLP TracerProvider.
	opencensus.InstallTraceBridge(opencensus.WithTracerProvider(o.tracerProvider))

	reader := sdkmetric.NewPeriodicReader(
		o.metricExporter,
		// Pull metrics from the OpenCensus views registered by the metrics Manager.
		sdkmetric.WithProducer(opencensus.NewMetricProducer()),
	)
	o.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(o.resource),
	)
	return nil
}

func (o *otlpExporterImpl) close() error {
	ctx := context.Background()
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// The providers own their exporters, so shutting a provider down also shuts down its exporter.
	// Fall back to closing the raw exporter when register() was never called.
	if o.meterProvider != nil {
		record(o.meterProvider.Shutdown(ctx))
	} else {
		record(o.metricExporter.Shutdown(ctx))
	}
	if o.tracerProvider != nil {
		record(o.tracerProvider.Shutdown(ctx))
	} else {
		record(o.traceExporter.Shutdown(ctx))
	}
	return firstErr
}

// parseOTLPHeaders converts a comma-separated list of key=value pairs into a header map.
func parseOTLPHeaders(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	headers := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if key := strings.TrimSpace(kv[0]); key != "" {
			headers[key] = strings.TrimSpace(kv[1])
		}
	}
	return headers
}
