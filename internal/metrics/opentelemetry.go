package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/bridge/opencensus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	otelProtocolGRPC         = "grpc"
	otelProtocolHTTPProtobuf = "http/protobuf"
	otelDefaultServiceName   = "ld-relay"
	otelInstrumentationName  = "github.com/launchdarkly/ld-relay/v8"
)

var otelExporterType exporterType = otelExporterTypeImpl{} //nolint:gochecknoglobals

type otelExporterTypeImpl struct{}

type otelExporterImpl struct {
	readers    []sdkmetric.Reader
	processors []sdktrace.SpanProcessor
	resource   *sdkresource.Resource
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

	impl := &otelExporterImpl{resource: res}

	if !oc.DisableTraces {
		traceExp, err := newOTLPTraceExporter(ctx, protocol, oc.Endpoint, oc.Insecure, headers)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
		impl.processors = append(impl.processors, sdktrace.NewBatchSpanProcessor(traceExp))
	}

	if !oc.DisableMetrics {
		metricExp, err := newOTLPMetricExporter(ctx, protocol, oc.Endpoint, oc.Insecure, headers)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
		}
		// The OpenCensus metric producer surfaces OpenCensus stats views as OpenTelemetry metrics so
		// existing Relay measurements (connections, requests, events) are reported via OTLP.
		reader := sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithProducer(opencensus.NewMetricProducer()),
		)
		impl.readers = append(impl.readers, reader)
	}

	return impl, nil
}

func (o *otelExporterImpl) metricReaders() []sdkmetric.Reader     { return o.readers }
func (o *otelExporterImpl) spanProcessors() []sdktrace.SpanProcessor { return o.processors }
func (o *otelExporterImpl) resourceAttributes() *sdkresource.Resource { return o.resource }
func (o *otelExporterImpl) register(ldlog.Loggers) error           { return nil }
func (o *otelExporterImpl) close() error                           { return nil }

func newOTLPTraceExporter(
	ctx context.Context,
	protocol, endpoint string,
	insecure bool,
	headers map[string]string,
) (*otlptrace.Exporter, error) {
	if protocol == otelProtocolHTTPProtobuf {
		opts := []otlptracehttp.Option{}
		if endpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(endpoint))
		}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(headers))
		}
		return otlptracehttp.New(ctx, opts...)
	}

	opts := []otlptracegrpc.Option{}
	if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(stripScheme(endpoint)))
	}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return otlptracegrpc.New(ctx, opts...)
}

func newOTLPMetricExporter(
	ctx context.Context,
	protocol, endpoint string,
	insecure bool,
	headers map[string]string,
) (sdkmetric.Exporter, error) {
	if protocol == otelProtocolHTTPProtobuf {
		opts := []otlpmetrichttp.Option{}
		if endpoint != "" {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(endpoint))
		}
		if insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(headers))
		}
		return otlpmetrichttp.New(ctx, opts...)
	}

	opts := []otlpmetricgrpc.Option{}
	if endpoint != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(stripScheme(endpoint)))
	}
	if insecure {
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
	return sdkresource.NewSchemaless(attrs...), nil
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
