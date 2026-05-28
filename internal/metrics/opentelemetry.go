package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/bridge/opencensus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	otelProtocolGRPC         = "grpc"
	otelProtocolHTTPProtobuf = "http/protobuf"
	otelDefaultServiceName   = "ld-relay"
	otelShutdownTimeout      = 5 * time.Second
)

var otelExporterType exporterType = otelExporterTypeImpl{} //nolint:gochecknoglobals

type otelExporterTypeImpl struct{}

type otelExporterImpl struct {
	tracerProvider  *sdktrace.TracerProvider
	meterProvider   *sdkmetric.MeterProvider
	previousTextMap propagation.TextMapPropagator
	tracesEnabled   bool
	metricsEnabled  bool
	loggers         ldlog.Loggers
}

func (o otelExporterTypeImpl) getName() string {
	return "OpenTelemetry"
}

func (o otelExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	oc := mc.OpenTelemetry
	if !oc.Enabled {
		return nil, nil
	}
	if oc.DisableTraces && oc.DisableMetrics {
		return nil, errors.New("USE_OPENTELEMETRY is true but both traces and metrics are disabled")
	}

	protocol := strings.ToLower(strings.TrimSpace(oc.Protocol))
	if protocol == "" {
		protocol = otelProtocolGRPC
	}
	if protocol != otelProtocolGRPC && protocol != otelProtocolHTTPProtobuf {
		return nil, fmt.Errorf("unsupported OPENTELEMETRY_PROTOCOL %q (expected %q or %q)", oc.Protocol, otelProtocolGRPC, otelProtocolHTTPProtobuf)
	}

	headers, err := parseOTLPHeaders(oc.Headers)
	if err != nil {
		return nil, err
	}

	res, err := buildOTelResource(oc)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	impl := &otelExporterImpl{
		tracesEnabled:  !oc.DisableTraces,
		metricsEnabled: !oc.DisableMetrics,
		loggers:        loggers,
	}

	if impl.tracesEnabled {
		traceExp, err := newOTLPTraceExporter(ctx, protocol, oc, headers)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
		impl.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExp),
			sdktrace.WithResource(res),
		)
	}

	if impl.metricsEnabled {
		metricExp, err := newOTLPMetricExporter(ctx, protocol, oc, headers)
		if err != nil {
			if impl.tracerProvider != nil {
				_ = impl.tracerProvider.Shutdown(context.Background())
			}
			return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
		}
		// The OpenCensus metric producer surfaces OpenCensus stats views as OpenTelemetry metrics so
		// existing Relay measurements (connections, requests, events) are reported via OTLP.
		reader := sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithProducer(opencensus.NewMetricProducer()),
		)
		impl.meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(reader),
			sdkmetric.WithResource(res),
		)
	}

	return impl, nil
}

func (o *otelExporterImpl) register() error {
	if o.tracerProvider != nil {
		otel.SetTracerProvider(o.tracerProvider)
		o.previousTextMap = otel.GetTextMapPropagator()
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		// Route OpenCensus spans through the OpenTelemetry tracer so existing OC instrumentation
		// inside Relay and its dependencies is exported via OTLP.
		opencensus.InstallTraceBridge(opencensus.WithTracerProvider(o.tracerProvider))
	}
	if o.meterProvider != nil {
		otel.SetMeterProvider(o.meterProvider)
	}
	return nil
}

func (o *otelExporterImpl) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), otelShutdownTimeout)
	defer cancel()

	var errs []error
	if o.tracerProvider != nil {
		if o.previousTextMap != nil {
			otel.SetTextMapPropagator(o.previousTextMap)
		}
		if err := o.tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
		}
	}
	if o.meterProvider != nil {
		if err := o.meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter provider shutdown: %w", err))
		}
	}
	return errors.Join(errs...)
}

func newOTLPTraceExporter(
	ctx context.Context,
	protocol string,
	oc config.OpenTelemetryConfig,
	headers map[string]string,
) (*otlptrace.Exporter, error) {
	if protocol == otelProtocolHTTPProtobuf {
		opts := []otlptracehttp.Option{}
		if oc.Endpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(oc.Endpoint))
		}
		if oc.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(headers))
		}
		return otlptracehttp.New(ctx, opts...)
	}

	opts := []otlptracegrpc.Option{}
	if oc.Endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(stripScheme(oc.Endpoint)))
	}
	if oc.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return otlptracegrpc.New(ctx, opts...)
}

func newOTLPMetricExporter(
	ctx context.Context,
	protocol string,
	oc config.OpenTelemetryConfig,
	headers map[string]string,
) (sdkmetric.Exporter, error) {
	if protocol == otelProtocolHTTPProtobuf {
		opts := []otlpmetrichttp.Option{}
		if oc.Endpoint != "" {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(oc.Endpoint))
		}
		if oc.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(headers))
		}
		return otlpmetrichttp.New(ctx, opts...)
	}

	opts := []otlpmetricgrpc.Option{}
	if oc.Endpoint != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(stripScheme(oc.Endpoint)))
	}
	if oc.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
	}
	return otlpmetricgrpc.New(ctx, opts...)
}

func buildOTelResource(oc config.OpenTelemetryConfig) (*sdkresource.Resource, error) {
	serviceName := strings.TrimSpace(oc.ServiceName)
	if serviceName == "" {
		serviceName = otelDefaultServiceName
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(serviceName)}
	if prefix := strings.TrimSpace(oc.Prefix); prefix != "" {
		attrs = append(attrs, semconv.ServiceNamespace(prefix))
	}
	// Merge with the SDK default resource so OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME are
	// still honored when set in the environment. Use NewSchemaless to avoid forcing a schema URL
	// onto the merge result, which would conflict with the default resource's schema URL.
	return sdkresource.Merge(sdkresource.Default(), sdkresource.NewSchemaless(attrs...))
}

func parseOTLPHeaders(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid OPENTELEMETRY_HEADERS entry %q (expected key=value)", pair)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("invalid OPENTELEMETRY_HEADERS entry %q (empty key)", pair)
		}
		out[k] = v
	}
	return out, nil
}

func stripScheme(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(endpoint, prefix) {
			return strings.TrimPrefix(endpoint, prefix)
		}
	}
	return endpoint
}

func otelInstrumentationName() string {
	return "github.com/launchdarkly/ld-relay/v8"
}
