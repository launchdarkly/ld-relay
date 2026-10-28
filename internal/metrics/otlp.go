package metrics

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/launchdarkly/ld-relay/v9/config"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func newOTLPExporters(
	otlpConfig config.OpenTelemetryConfig,
	logger *slog.Logger,
) ([]sdkmetric.Option, error) {
	protocol := otlpConfig.ExportProtocol()

	ctx := context.Background()

	var metricExporter sdkmetric.Exporter
	var err error

	switch protocol {
	case config.OTLPProtocolGRPC:
		metricExporter, err = otlpmetricgrpc.New(ctx)
	case config.OTLPProtocolHTTPProtobuf:
		metricExporter, err = otlpmetrichttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol: %q (must be \"grpc\" or \"http/protobuf\")", protocol)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP %s metric exporter: %w", protocol, err)
	}

	opts := []sdkmetric.Option{sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter))}

	logger.Info("successfully registered OTLP metrics exporter", "protocol", protocol)

	return opts, nil
}
