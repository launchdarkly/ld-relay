package metrics

import (
	"fmt"
	"net/http"

	"go.opencensus.io/exporter/prometheus"
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

	exporter, err := prometheus.NewExporter(prometheus.Options{Namespace: o.Prefix})
	if err != nil {
		return err
	}
	http.Handle("/metrics", exporter)
	go func() {
		err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
		if err != nil {
			logging.Error.Printf("Failed to start Prometheus listener\n")
		} else {
			logging.Info.Printf("Prometheus listening on port %d\n", port)
		}
	}()
	view.RegisterExporter(exporter)
	return nil
}
