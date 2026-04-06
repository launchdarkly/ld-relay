package sharedtest

import (
	"context"
	"sync"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestMetricsReader is a test helper that allows reading OTel metric data.
type TestMetricsReader struct {
	reader sdkmetric.Reader
	lock   sync.Mutex
}

// NewTestMetricsReader creates a TestMetricsReader with an OTel ManualReader.
func NewTestMetricsReader() *TestMetricsReader {
	return &TestMetricsReader{
		reader: sdkmetric.NewManualReader(),
	}
}

// GetReader returns the sdkmetric.Reader for use in MeterProvider construction.
func (r *TestMetricsReader) GetReader() sdkmetric.Reader {
	return r.reader
}

// CollectMetrics collects the current metric data from the reader.
func (r *TestMetricsReader) CollectMetrics() (*metricdata.ResourceMetrics, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	var rm metricdata.ResourceMetrics
	err := r.reader.Collect(context.Background(), &rm)
	return &rm, err
}

// FindMetricByName searches resource metrics for a metric with the given name and returns its data points.
func FindMetricByName(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}
