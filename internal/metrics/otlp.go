package metrics

import (
	"context"
	"fmt"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v9/config"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func newOTLPExporters(
	otlpConfig config.OpenTelemetryConfig,
	loggers ldlog.Loggers,
) ([]sdkmetric.Option, error) {
	protocol := strings.ToLower(otlpConfig.Protocol)
	if protocol == "" {
		protocol = "grpc"
	}

	ctx := context.Background()
	headers := parseHeaders(otlpConfig.Headers)

	var metricExporter sdkmetric.Exporter
	var err error

	switch protocol {
	case "grpc":
		metricExporter, err = otlpmetricgrpc.New(ctx, buildGRPCMetricOptions(otlpConfig, headers)...)
	case "http":
		metricExporter, err = otlpmetrichttp.New(ctx, buildHTTPMetricOptions(otlpConfig, headers)...)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol: %q (must be \"grpc\" or \"http\")", protocol)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP %s metric exporter: %w", protocol, err)
	}

	opts := []sdkmetric.Option{sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter))}

	loggers.Infof("Successfully registered OTLP metrics exporter (protocol=%s, endpoint=%s)", protocol, otlpConfig.Endpoint)

	return opts, nil
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
		opts = append(opts, otlpmetricgrpc.WithEndpointURL(otlpConfig.Endpoint))
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
	}
	return opts
}

func buildHTTPMetricOptions(otlpConfig config.OpenTelemetryConfig, headers map[string]string) []otlpmetrichttp.Option {
	var opts []otlpmetrichttp.Option
	if otlpConfig.Endpoint != "" {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(otlpConfig.Endpoint))
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(headers))
	}
	return opts
}
