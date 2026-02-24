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

	var metricReader *sdkmetric.PeriodicReader

	switch protocol {
	case "grpc":
		// Metric exporter
		grpcOpts := []otlpmetricgrpc.Option{}
		if otlpConfig.Endpoint != "" {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpoint(otlpConfig.Endpoint))
		}
		if otlpConfig.Insecure {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithInsecure())
		}
		if otlpConfig.Headers != "" {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithHeaders(parseHeaders(otlpConfig.Headers)))
		}

		metricExporter, err := otlpmetricgrpc.New(ctx, grpcOpts...)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create OTLP gRPC metric exporter: %w", err)
		}
		metricReader = sdkmetric.NewPeriodicReader(metricExporter)
		opts = append(opts, sdkmetric.WithReader(metricReader))

		// Trace exporter (if enabled)
		if otlpConfig.Traces {
			traceOpts := []otlptracegrpc.Option{}
			if otlpConfig.Endpoint != "" {
				traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
			}
			if otlpConfig.Headers != "" {
				traceOpts = append(traceOpts, otlptracegrpc.WithHeaders(parseHeaders(otlpConfig.Headers)))
			}

			traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
			if err != nil {
				_ = metricReader.Shutdown(ctx)
				return nil, nil, fmt.Errorf("failed to create OTLP gRPC trace exporter: %w", err)
			}
			tracerProvider = sdktrace.NewTracerProvider(
				sdktrace.WithBatcher(traceExporter),
				sdktrace.WithResource(res),
			)
		}

	case "http":
		// Metric exporter
		httpOpts := []otlpmetrichttp.Option{}
		if otlpConfig.Endpoint != "" {
			httpOpts = append(httpOpts, otlpmetrichttp.WithEndpoint(otlpConfig.Endpoint))
		}
		if otlpConfig.Insecure {
			httpOpts = append(httpOpts, otlpmetrichttp.WithInsecure())
		}
		if otlpConfig.Headers != "" {
			httpOpts = append(httpOpts, otlpmetrichttp.WithHeaders(parseHeaders(otlpConfig.Headers)))
		}

		metricExporter, err := otlpmetrichttp.New(ctx, httpOpts...)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create OTLP HTTP metric exporter: %w", err)
		}
		metricReader = sdkmetric.NewPeriodicReader(metricExporter)
		opts = append(opts, sdkmetric.WithReader(metricReader))

		// Trace exporter (if enabled)
		if otlpConfig.Traces {
			traceOpts := []otlptracehttp.Option{}
			if otlpConfig.Endpoint != "" {
				traceOpts = append(traceOpts, otlptracehttp.WithEndpoint(otlpConfig.Endpoint))
			}
			if otlpConfig.Insecure {
				traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
			}
			if otlpConfig.Headers != "" {
				traceOpts = append(traceOpts, otlptracehttp.WithHeaders(parseHeaders(otlpConfig.Headers)))
			}

			traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
			if err != nil {
				_ = metricReader.Shutdown(ctx)
				return nil, nil, fmt.Errorf("failed to create OTLP HTTP trace exporter: %w", err)
			}
			tracerProvider = sdktrace.NewTracerProvider(
				sdktrace.WithBatcher(traceExporter),
				sdktrace.WithResource(res),
			)
		}

	default:
		return nil, nil, fmt.Errorf("unsupported OTLP protocol: %q (must be \"grpc\" or \"http\")", protocol)
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
