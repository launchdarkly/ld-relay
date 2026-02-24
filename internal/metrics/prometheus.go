package metrics

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

type prometheusServer struct {
	reader   sdkmetric.Reader
	server   *http.Server
	listener net.Listener
	loggers  ldlog.Loggers
}

func newPrometheusExporter(promConfig config.PrometheusConfig, loggers ldlog.Loggers) (*prometheusServer, error) {
	port := promConfig.Port.GetOrElse(config.DefaultPrometheusPort)

	registry := prometheus.NewRegistry()
	exporter, err := promexporter.New(
		promexporter.WithRegisterer(registry),
		promexporter.WithNamespace(getPrefix(promConfig.Prefix)),
		promexporter.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
	)
	if err != nil {
		return nil, err
	}

	exporterMux := http.NewServeMux()
	exporterMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           exporterMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Separate Listen and Serve here so we can immediately detect if the port isn't available
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, fmt.Errorf("failed to start Prometheus listener: %w", err)
	}

	ps := &prometheusServer{
		reader:   exporter,
		server:   server,
		listener: listener,
		loggers:  loggers,
	}

	go func() {
		err := server.Serve(listener)
		if err != http.ErrServerClosed {
			loggers.Errorf("Prometheus listener failed: %s", err)
		}
	}()

	loggers.Infof("Successfully registered Prometheus metrics exporter on port %d", port)

	return ps, nil
}

func (ps *prometheusServer) close() {
	if ps.server != nil {
		_ = ps.server.Close()
	}
	if ps.listener != nil {
		_ = ps.listener.Close()
	}
}

func getPrefix(configuredPrefix string) string {
	if configuredPrefix != "" {
		return configuredPrefix
	}
	return defaultMetricsPrefix
}
