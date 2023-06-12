package metrics

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v7/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"contrib.go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats/view"
)

func errPrometheusListenerFailed(err error) error {
	return fmt.Errorf("failed to start Prometheus listener: %w", err)
}

var prometheusExporterType exporterType = prometheusExporterTypeImpl{} //nolint:gochecknoglobals

type prometheusExporterTypeImpl struct{}

type prometheusExporterImpl struct {
	exporter *prometheus.Exporter
	server   *http.Server
	listener net.Listener
	loggers  ldlog.Loggers
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

	logPrometheusError := func(e error) { // COVERAGE: can't make this happen in unit tests
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
		return nil, err // COVERAGE: can't make this happen in unit tests
	}

	exporterMux := http.NewServeMux()
	exporterMux.Handle("/metrics", exporter)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           exporterMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return &prometheusExporterImpl{
		exporter: exporter,
		server:   server,
		loggers:  loggers,
	}, nil
}

func (p *prometheusExporterImpl) register() error {
	// Separate Listen and Serve here instead of calling ListenAndServe() so that we can immediately
	// detect if the port isn't available
	listener, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return errPrometheusListenerFailed(err)
	}
	p.listener = listener
	go func() {
		err := p.server.Serve(p.listener)
		if err != http.ErrServerClosed { // Serve never returns a nil error value
			p.loggers.Error(errPrometheusListenerFailed(err)) // COVERAGE: can't make this happen in unit tests
		}
	}()

	view.RegisterExporter(p.exporter)
	// Note: we do not call trace.RegisterExporter for the Prometheus exporter, because the different
	// semantics of Prometheus (their agent calls our endpoint) makes trace inapplicable.

	return nil
}

func (p *prometheusExporterImpl) close() error {
	view.UnregisterExporter(p.exporter)
	err := p.server.Close()
	if p.listener != nil {
		_ = p.listener.Close()
	}
	return err
}
