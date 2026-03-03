package metrics

import (
	"context"
	"fmt"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newOTLPExporters(
	otlpConfig config.OpenTelemetryConfig,
	res *resource.Resource,
	loggers ldlog.Loggers,
) ([]sdkmetric.Option, *sdktrace.TracerProvider, error) {
	var opts []sdkmetric.Option
	var tracerProvider *sdktrace.TracerProvider

	protocol := strings.ToLower(otlpConfig.Protocol)
	if protocol == "" {
		protocol = "grpc"
	}

	ctx := context.Background()

	type metricExporterFactory func(ctx context.Context) (sdkmetric.Exporter, error)
	type traceExporterFactory func(ctx context.Context) (sdktrace.SpanExporter, error)

	var (
		createMetricExporter metricExporterFactory
		createTraceExporter  traceExporterFactory
	)

	headers := parseHeaders(otlpConfig.Headers)

	switch protocol {
	case "grpc":
		createMetricExporter = func(ctx context.Context) (sdkmetric.Exporter, error) {
			var gopts []otlpmetricgrpc.Option
			if otlpConfig.Endpoint != "" {
				gopts = append(gopts, otlpmetricgrpc.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				gopts = append(gopts, otlpmetricgrpc.WithInsecure())
			}
			if len(headers) > 0 {
				gopts = append(gopts, otlpmetricgrpc.WithHeaders(headers))
			}
			return otlpmetricgrpc.New(ctx, gopts...)
		}
		createTraceExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
			var gopts []otlptracegrpc.Option
			if otlpConfig.Endpoint != "" {
				gopts = append(gopts, otlptracegrpc.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				gopts = append(gopts, otlptracegrpc.WithInsecure())
			}
			if len(headers) > 0 {
				gopts = append(gopts, otlptracegrpc.WithHeaders(headers))
			}
			return otlptracegrpc.New(ctx, gopts...)
		}
	case "http":
		createMetricExporter = func(ctx context.Context) (sdkmetric.Exporter, error) {
			var hopts []otlpmetrichttp.Option
			if otlpConfig.Endpoint != "" {
				hopts = append(hopts, otlpmetrichttp.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				hopts = append(hopts, otlpmetrichttp.WithInsecure())
			}
			if len(headers) > 0 {
				hopts = append(hopts, otlpmetrichttp.WithHeaders(headers))
			}
			return otlpmetrichttp.New(ctx, hopts...)
		}
		createTraceExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
			var hopts []otlptracehttp.Option
			if otlpConfig.Endpoint != "" {
				hopts = append(hopts, otlptracehttp.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				hopts = append(hopts, otlptracehttp.WithInsecure())
			}
			if len(headers) > 0 {
				hopts = append(hopts, otlptracehttp.WithHeaders(headers))
			}
			return otlptracehttp.New(ctx, hopts...)
		}
	default:
		return nil, nil, fmt.Errorf("unsupported OTLP protocol: %q (must be \"grpc\" or \"http\")", protocol)
	}

	metricExporter, err := createMetricExporter(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OTLP %s metric exporter: %w", protocol, err)
	}
	metricReader := sdkmetric.NewPeriodicReader(metricExporter)
	opts = append(opts, sdkmetric.WithReader(metricReader))

	if otlpConfig.Traces {
		traceExporter, err := createTraceExporter(ctx)
		if err != nil {
			_ = metricReader.Shutdown(ctx)
			return nil, nil, fmt.Errorf("failed to create OTLP %s trace exporter: %w", protocol, err)
		}
		tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)
	}

	loggers.Infof("Successfully registered OTLP metrics exporter (protocol=%s, endpoint=%s)", protocol, otlpConfig.Endpoint)

	return opts, tracerProvider, nil
}

func parseHeaders(headers string) map[string]string {
	result := make(map[string]string)
	for _, pair := range strings.Split(headers, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}
