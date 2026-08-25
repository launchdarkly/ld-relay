package metrics

import (
	"context"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collect gathers everything the reader has and returns the metrics by name.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func sumPoints(t *testing.T, m metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "%s must be an int64 sum", m.Name)
	return sum.DataPoints
}

func pointWith(t *testing.T, points []metricdata.DataPoint[int64], kv attribute.KeyValue) metricdata.DataPoint[int64] {
	t.Helper()
	for _, p := range points {
		if v, ok := p.Attributes.Value(kv.Key); ok && v == kv.Value {
			return p
		}
	}
	require.Failf(t, "missing data point", "no point with %v=%v", kv.Key, kv.Value)
	return metricdata.DataPoint[int64]{}
}

func TestInitInstrumentsRecordTheSynchronousMeasurements(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	ii := newInitInstruments(meter)

	ii.RecordDelivery("fdv2", "completed", false)
	ii.RecordDelivery("fdv2", "connection_ended", true)
	ii.RecordDelivery("fdv1", "read_error", false)
	ii.RecordUpToDate(false)
	ii.RecordUpToDate(true)
	ii.RecordShed()
	ii.RecordPollShed("Production Env")
	ii.AddDeadlineSetErrors(3)
	ii.AddDeadlineSetErrors(0) // must not create a data point

	ms := collect(t, reader)

	deliveries := sumPoints(t, ms[initDeliveriesMeasureName])
	assert.Len(t, deliveries, 3)
	assert.Equal(t, int64(1), pointWith(t, deliveries, initOutcomeAttrKey.String("connection_ended")).Value)
	p := pointWith(t, deliveries, initOutcomeAttrKey.String("connection_ended"))
	capVal, _ := p.Attributes.Value(initCapEngagedAttrKey)
	assert.True(t, capVal.AsBool(), "the cap_engaged attribute must survive")

	upToDate := sumPoints(t, ms[initUpToDateMeasureName])
	assert.Equal(t, int64(1), pointWith(t, upToDate, initAfterWaitAttrKey.Bool(true)).Value)
	assert.Equal(t, int64(1), pointWith(t, upToDate, initAfterWaitAttrKey.Bool(false)).Value)

	sheds := sumPoints(t, ms[initShedsMeasureName])
	assert.Equal(t, int64(1), pointWith(t, sheds, initTransportAttrKey.String("stream")).Value)
	pollShed := pointWith(t, sheds, initTransportAttrKey.String("poll"))
	envVal, ok := pollShed.Attributes.Value(envNameAttrKey)
	require.True(t, ok, "a poll shed must carry the environment name")
	assert.NotEmpty(t, envVal.AsString())

	errs := sumPoints(t, ms[initDeadlineSetErrorsMeasureName])
	require.Len(t, errs, 1)
	assert.Equal(t, int64(3), errs[0].Value)
}

func TestRegisterInitConcurrencyObserversReadTheLimiter(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	m := &Manager{meter: meter}

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 2, MaxQueued: 1})
	require.NoError(t, m.RegisterInitConcurrencyObservers(limiter))

	// Drive the limiter to a known state: two held slots, one rejection of each cause.
	r1, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	_, ok = limiter.Acquire(context.Background())
	require.True(t, ok)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok = limiter.Acquire(gone) // client_gone
	require.False(t, ok)
	// Fill the queue, then overflow it for a budget_full rejection.
	go func() {
		if r, ok := limiter.Acquire(context.Background()); ok {
			r()
		}
	}()
	for limiter.Stats().Waiting != 1 {
	}
	_, ok = limiter.Acquire(context.Background()) // budget_full
	require.False(t, ok)

	ms := collect(t, reader)
	heldGauge, ok2 := ms[initSlotsHeldMeasureName].Data.(metricdata.Gauge[int64])
	require.True(t, ok2)
	assert.Equal(t, int64(2), heldGauge.DataPoints[0].Value)

	waitingGauge, _ := ms[initQueueWaitingMeasureName].Data.(metricdata.Gauge[int64])
	assert.Equal(t, int64(1), waitingGauge.DataPoints[0].Value)

	admitted := sumPoints(t, ms[initAdmittedMeasureName])
	require.Len(t, admitted, 1)
	assert.Equal(t, int64(2), admitted[0].Value)

	rejected := sumPoints(t, ms[initRejectedMeasureName])
	assert.Equal(t, int64(1), pointWith(t, rejected, initReasonAttrKey.String("budget_full")).Value)
	assert.Equal(t, int64(1), pointWith(t, rejected, initReasonAttrKey.String("client_gone")).Value)
	assert.Equal(t, int64(0), pointWith(t, rejected, initReasonAttrKey.String("shutdown")).Value)

	r1()
	limiter.Close()
}

func TestInitInstrumentsAreSafeWhenDisabled(t *testing.T) {
	// When OpenTelemetry is disabled the recorder is nil; every method must do nothing.
	var ii *InitInstruments
	assert.NotPanics(t, func() {
		ii.RecordDelivery("fdv2", "completed", false)
		ii.RecordUpToDate(true)
		ii.RecordShed()
		ii.RecordPollShed("env")
		ii.AddDeadlineSetErrors(1)
	})
	m := &Manager{} // no meter: registration must be a quiet no-op
	assert.NoError(t, m.RegisterInitConcurrencyObservers(concurrency.New("t", concurrency.Params{MaxConcurrent: 1})))
}
