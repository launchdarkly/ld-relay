package metrics

import (
	"fmt"
	"net/http"

	"contrib.go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats/view"

	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

func init() {
	defineExporter(prometheusExporter, registerPrometheusExporter)
}

func registerPrometheusExporter(options ExporterOptions) error {
	o := options.(PrometheusOptions)
	port := 8031
	if o.Port != 0 {
		port = o.Port
	}

	logPrometheusError := func(e error) {
		logging.GlobalLoggers.Errorf("Prometheus exporter error: %s", e)
	}

	exporter, err := prometheus.NewExporter(prometheus.Options{Namespace: o.Prefix, OnError: logPrometheusError})
	if err != nil {
		return err
	}
	http.Handle("/metrics", exporter)
	go func() {
		err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
		if err != nil {
			logging.GlobalLoggers.Errorf("Failed to start Prometheus listener\n")
		} else {
			logging.GlobalLoggers.Infof("Prometheus listening on port %d\n", port)
		}
	}()
	view.RegisterExporter(exporter)
	return nil
}
