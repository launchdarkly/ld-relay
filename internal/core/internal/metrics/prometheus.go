package metrics

import (
	"fmt"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/logging"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"contrib.go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats/view"
)

var prometheusExporterType exporterType = prometheusExporterTypeImpl{} //nolint:gochecknoglobals

type prometheusExporterTypeImpl struct{}

type prometheusExporterImpl struct {
	exporter *prometheus.Exporter
	server   *http.Server
}

func (p prometheusExporterTypeImpl) getName() string {
	return "Prometheus"
}

func (p prometheusExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	if !mc.Prometheus.Enabled {
		return nil, nil
	}

	port := mc.Prometheus.Port.GetOrElse(config.DefaultPrometheusPort)

	logPrometheusError := func(e error) {
		loggers.Errorf("Prometheus exporter error: %s", e)
	}

	options := prometheus.Options{
		Namespace: getPrefix(mc.Prometheus.Prefix),
		OnError:   logPrometheusError,
	}
	exporter, err := prometheus.NewExporter(options)

	// The current implementation of prometheus.NewExporter() apparently can never return a non-nil error,
	// but in case it does in the future, we should check it
	if err != nil {
		return nil, err
	}

	exporterMux := http.NewServeMux()
	exporterMux.Handle("/metrics", exporter)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: exporterMux,
	}

	return &prometheusExporterImpl{
		exporter: exporter,
		server:   server,
	}, nil
}

func (p *prometheusExporterImpl) register() error {
	go func() {
		err := p.server.ListenAndServe()
		if err != http.ErrServerClosed { // ListenAndServe never returns a nil error value
			logging.MakeDefaultLoggers().Errorf("Failed to start Prometheus listener\n")
		}
	}()

	view.RegisterExporter(p.exporter)
	// Note: we do not call trace.RegisterExporter for the Prometheus exporter, because the different
	// semantics of Prometheus (their agent calls our endpoint) makes trace inapplicable.

	return nil
}

func (p *prometheusExporterImpl) close() error {
	view.UnregisterExporter(p.exporter)
	return p.server.Close()
}
