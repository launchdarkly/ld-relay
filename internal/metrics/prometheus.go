package metrics

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/bridge/opencensus"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func errPrometheusListenerFailed(err error) error {
	return fmt.Errorf("failed to start Prometheus listener: %w", err)
}

var prometheusExporterType exporterType = prometheusExporterTypeImpl{} //nolint:gochecknoglobals

type prometheusExporterTypeImpl struct{}

type prometheusExporterImpl struct {
	reader   sdkmetric.Reader
	server   *http.Server
	listener net.Listener
}

func (p prometheusExporterTypeImpl) getName() string {
	return "Prometheus"
}

func (p prometheusExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	_ ldlog.Loggers,
) (exporter, error) {
	if !mc.Prometheus.Enabled {
		return nil, nil
	}

	registry := prometheus.NewRegistry()
	opts := []otelprom.Option{
		otelprom.WithRegisterer(registry),
		otelprom.WithProducer(opencensus.NewMetricProducer()),
	}
	if prefix := getPrefix(mc.Prometheus.Prefix); prefix != "" {
		opts = append(opts, otelprom.WithNamespace(prefix))
	}
	reader, err := otelprom.New(opts...)
	if err != nil {
		return nil, err
	}

	port := mc.Prometheus.Port.GetOrElse(config.DefaultPrometheusPort)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry}))

	return &prometheusExporterImpl{
		reader: reader,
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

func (p *prometheusExporterImpl) metricReaders() []sdkmetric.Reader     { return []sdkmetric.Reader{p.reader} }
func (p *prometheusExporterImpl) spanProcessors() []sdktrace.SpanProcessor { return nil }
func (p *prometheusExporterImpl) resourceAttributes() *sdkresource.Resource { return nil }

func (p *prometheusExporterImpl) register(loggers ldlog.Loggers) error {
	listener, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return errPrometheusListenerFailed(err)
	}
	p.listener = listener
	go func() {
		if err := p.server.Serve(p.listener); err != nil && err != http.ErrServerClosed {
			loggers.Error(errPrometheusListenerFailed(err))
		}
	}()
	return nil
}

func (p *prometheusExporterImpl) close() error {
	err := p.server.Close()
	if p.listener != nil {
		_ = p.listener.Close()
	}
	return err
}
