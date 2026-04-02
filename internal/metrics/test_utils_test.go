package metrics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/events"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testMetricsRelayID = "test-metrics-relay-id"
	userAgentValue     = "my-agent"
)

type testWithOTelParams struct {
	manager     *Manager
	relayID     string
	envName     string
	env         *EnvironmentManager
	instruments *Instruments
	reader      sdkmetric.Reader
	mockLog     *ldlogtest.MockLog
}

// collectMetrics reads the current metrics from the test reader.
func (p testWithOTelParams) collectMetrics() (*metricdata.ResourceMetrics, error) {
	var rm metricdata.ResourceMetrics
	err := p.reader.Collect(context.Background(), &rm)
	return &rm, err
}

func testWithOTel(t *testing.T, action func(testWithOTelParams)) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	// Create a ManualReader for the test
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := meterProvider.Meter("ld-relay")

	instruments, err := NewInstrumentsForTest(meter)
	require.NoError(t, err)

	manager, err := NewManager(config.OpenTelemetryConfig{}, time.Millisecond*10, mockLog.Loggers)
	require.NoError(t, err)
	defer manager.Close()

	// Override the instruments on the manager with our test ones
	manager.instruments = instruments

	// Since OTel doesn't have global state like OpenCensus, we just use a randomized
	// environment name for test isolation.
	envName := "env-" + uuid.New()

	env, err := manager.AddEnvironment(envName, nil)
	require.NoError(t, err)

	action(testWithOTelParams{
		manager:     manager,
		relayID:     manager.metricsRelayID,
		envName:     envName,
		env:         env,
		instruments: instruments,
		reader:      reader,
		mockLog:     mockLog,
	})
}

type testEventsPublisher struct {
	events chan json.RawMessage
}

func newTestEventsPublisher() *testEventsPublisher {
	return &testEventsPublisher{
		events: make(chan json.RawMessage, 100),
	}
}

func (p *testEventsPublisher) Publish(context events.EventPayloadMetadata, events ...json.RawMessage) {
	for _, e := range events {
		p.events <- e
	}
}
func (p *testEventsPublisher) Flush()                                     {}
func (p *testEventsPublisher) Close()                                     {}
func (p *testEventsPublisher) ReplaceCredential(credential.SDKCredential) {}

func (p *testEventsPublisher) expectMetricsEvent(t *testing.T, timeout time.Duration) relayMetricsEvent {
	if ret, ok := p.maybeReceiveMetricsEvent(t, timeout); ok {
		return ret
	}
	require.Fail(t, "timed out waiting for metrics event")
	return relayMetricsEvent{}
}

func (p *testEventsPublisher) maybeReceiveMetricsEvent(t *testing.T, timeout time.Duration) (relayMetricsEvent, bool) {
	eventData, ok, _ := helpers.TryReceive(p.events, timeout)
	if ok {
		var metricsEvent relayMetricsEvent
		require.NoError(t, json.Unmarshal(eventData, &metricsEvent))
		return metricsEvent, true
	}
	return relayMetricsEvent{}, false
}

func (p *testEventsPublisher) expectNoMetricsEvent(t *testing.T, timeout time.Duration) {
	if !helpers.AssertNoMoreValues(t, p.events, timeout, "received unexpected metrics event") {
		t.FailNow()
	}
}

func (p *testEventsPublisher) expectUsageEvent(t *testing.T, timeout time.Duration) relayUsageEvent {
	if ret, ok := p.maybeReceiveUsageEvent(t, timeout); ok {
		return ret
	}
	require.Fail(t, "timed out waiting for metrics event")
	return relayUsageEvent{}
}

func (p *testEventsPublisher) maybeReceiveUsageEvent(t *testing.T, timeout time.Duration) (relayUsageEvent, bool) {
	eventData, ok, _ := helpers.TryReceive(p.events, timeout)
	if ok {
		var metricsEvent relayUsageEvent
		require.NoError(t, json.Unmarshal(eventData, &metricsEvent))
		return metricsEvent, true
	}
	return relayUsageEvent{}, false
}

func (p *testEventsPublisher) expectNoUsageEvent(t *testing.T, timeout time.Duration) {
	if !helpers.AssertNoMoreValues(t, p.events, timeout, "received unexpected metrics event") {
		t.FailNow()
	}
}
