package sharedtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestMetricsReader is a test helper that allows reading OTel metric data.
type TestMetricsReader struct {
	reader       sdkmetric.Reader
	spanExporter *tracetest.InMemoryExporter
	lock         sync.Mutex
}

// NewTestMetricsReader creates a TestMetricsReader with an OTel ManualReader and InMemoryExporter.
func NewTestMetricsReader() *TestMetricsReader {
	return &TestMetricsReader{
		reader:       sdkmetric.NewManualReader(),
		spanExporter: tracetest.NewInMemoryExporter(),
	}
}

// GetReader returns the sdkmetric.Reader for use in MeterProvider construction.
func (r *TestMetricsReader) GetReader() sdkmetric.Reader {
	return r.reader
}

// GetTracerProvider returns a TracerProvider that exports to the in-memory span exporter.
func (r *TestMetricsReader) GetTracerProvider() *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(r.spanExporter),
	)
}

// CollectMetrics collects the current metric data from the reader.
func (r *TestMetricsReader) CollectMetrics() (*metricdata.ResourceMetrics, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	var rm metricdata.ResourceMetrics
	err := r.reader.Collect(context.Background(), &rm)
	return &rm, err
}

// AwaitSpan waits for a span to be exported within the given timeout.
func (r *TestMetricsReader) AwaitSpan(t *testing.T, timeout time.Duration) tracetest.SpanStub {
	deadline := time.After(timeout)
	for {
		spans := r.spanExporter.GetSpans()
		if len(spans) > 0 {
			return spans[0]
		}
		select {
		case <-deadline:
			require.Fail(t, "timed out waiting for span data")
			return tracetest.SpanStub{}
		case <-time.After(time.Millisecond * 10):
		}
	}
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
