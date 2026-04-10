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

	var metricExporter sdkmetric.Exporter
	var err error

	switch protocol {
	case "grpc":
		metricExporter, err = otlpmetricgrpc.New(ctx)
	case "http":
		metricExporter, err = otlpmetrichttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol: %q (must be \"grpc\" or \"http\")", protocol)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP %s metric exporter: %w", protocol, err)
	}

	opts := []sdkmetric.Option{sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter))}

	loggers.Infof("Successfully registered OTLP metrics exporter (protocol=%s)", protocol)

	return opts, nil
}
