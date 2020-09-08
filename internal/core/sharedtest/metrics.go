package sharedtest

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/require"
	"go.opencensus.io/stats/view"
)

// TestMetricsExporter accumulates OpenCensus metrics for tests. It deaggregates the view data to make it
// easier to test for a specific row that we expect to see in the data.
type TestMetricsExporter struct {
	dataCh   chan TestMetricsData
	lastData TestMetricsData
	lock     sync.Mutex
}

// TestMetricsData is a map of OpenCensus view names to row data.
type TestMetricsData map[string][]TestMetricsRow

// HasRow returns true if this row exists for the specified view name.
func (d TestMetricsData) HasRow(viewName string, expectedRow TestMetricsRow) bool {
	for _, r := range d[viewName] {
		if reflect.DeepEqual(r, expectedRow) {
			return true
		}
	}
	return false
}

// TestMetricsRow is a simplified version of an OpenCensus view row.
type TestMetricsRow struct {
	Tags  map[string]string
	Count int64
	Sum   float64
}

// NewTestMetricsExporter creates a TestMetricsExporter.
func NewTestMetricsExporter() *TestMetricsExporter {
	return &TestMetricsExporter{dataCh: make(chan TestMetricsData, 10), lastData: make(TestMetricsData)}
}

// ExportView is called by OpenCensus.
func (e *TestMetricsExporter) ExportView(viewData *view.Data) {
	e.lock.Lock()
	defer e.lock.Unlock()

	viewName := viewData.View.Name
	rows := []TestMetricsRow{}
	for _, vr := range viewData.Rows {
		tr := TestMetricsRow{Tags: make(map[string]string, len(vr.Tags))}
		for _, t := range vr.Tags {
			tr.Tags[t.Key.Name()] = t.Value
		}

		if sumData, ok := vr.Data.(*view.SumData); ok {
			tr.Sum = sumData.Value
		}
		if countData, ok := vr.Data.(*view.CountData); ok {
			tr.Count = countData.Value
		}
		rows = append(rows, tr)
	}

	if !reflect.DeepEqual(rows, e.lastData[viewName]) {
		e.lastData[viewName] = rows
		dataCopy := make(TestMetricsData)
		for k, v := range e.lastData {
			dataCopy[k] = v
		}
		e.dataCh <- dataCopy
	}
}

// AwaitData waits until matching view data is received.
func (e *TestMetricsExporter) AwaitData(t *testing.T, timeout time.Duration, loggers ldlog.Loggers, fn func(TestMetricsData) bool) {
	deadline := time.After(timeout)
	for {
		select {
		case d := <-e.dataCh:
			loggers.Infof("exporter got metrics: %+v", d)
			if fn(d) {
				return
			}
		case <-deadline:
			require.Fail(t, "timed out waiting for metrics data")
		}
	}
}