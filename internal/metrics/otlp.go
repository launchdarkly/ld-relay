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
			return otlpmetricgrpc.New(ctx, buildGRPCMetricOptions(otlpConfig, headers)...)
		}
		createTraceExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
			return otlptracegrpc.New(ctx, buildGRPCTraceOptions(otlpConfig, headers)...)
		}
	case "http":
		createMetricExporter = func(ctx context.Context) (sdkmetric.Exporter, error) {
			return otlpmetrichttp.New(ctx, buildHTTPMetricOptions(otlpConfig, headers)...)
		}
		createTraceExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
			return otlptracehttp.New(ctx, buildHTTPTraceOptions(otlpConfig, headers)...)
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

func buildGRPCMetricOptions(otlpConfig config.OpenTelemetryConfig, headers map[string]string) []otlpmetricgrpc.Option {
	var opts []otlpmetricgrpc.Option
	if otlpConfig.Endpoint != "" {
		if strings.Contains(otlpConfig.Endpoint, "://") {
			opts = append(opts, otlpmetricgrpc.WithEndpointURL(otlpConfig.Endpoint))
		} else {
			opts = append(opts, otlpmetricgrpc.WithEndpoint(otlpConfig.Endpoint))
		}
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
	}
	return opts
}

func buildGRPCTraceOptions(otlpConfig config.OpenTelemetryConfig, headers map[string]string) []otlptracegrpc.Option {
	var opts []otlptracegrpc.Option
	if otlpConfig.Endpoint != "" {
		if strings.Contains(otlpConfig.Endpoint, "://") {
			opts = append(opts, otlptracegrpc.WithEndpointURL(otlpConfig.Endpoint))
		} else {
			opts = append(opts, otlptracegrpc.WithEndpoint(otlpConfig.Endpoint))
		}
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return opts
}

func buildHTTPMetricOptions(otlpConfig config.OpenTelemetryConfig, headers map[string]string) []otlpmetrichttp.Option {
	var opts []otlpmetrichttp.Option
	if otlpConfig.Endpoint != "" {
		if strings.Contains(otlpConfig.Endpoint, "://") {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(otlpConfig.Endpoint))
		} else {
			opts = append(opts, otlpmetrichttp.WithEndpoint(otlpConfig.Endpoint))
		}
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(headers))
	}
	return opts
}

func buildHTTPTraceOptions(otlpConfig config.OpenTelemetryConfig, headers map[string]string) []otlptracehttp.Option {
	var opts []otlptracehttp.Option
	if otlpConfig.Endpoint != "" {
		if strings.Contains(otlpConfig.Endpoint, "://") {
			opts = append(opts, otlptracehttp.WithEndpointURL(otlpConfig.Endpoint))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(otlpConfig.Endpoint))
		}
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	return opts
}
