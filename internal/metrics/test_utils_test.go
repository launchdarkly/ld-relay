package metrics

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	st "github.com/launchdarkly/ld-relay/v6/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
)

const (
	testMetricsRelayID = "test-metrics-relay-id"
	userAgentValue     = "my-agent"
)

type testWithExporterParams struct {
	exporter *st.TestMetricsExporter
	relayID  string
	envName  string
	env      *EnvironmentManager
	mockLog  *ldlogtest.MockLog
}

func testWithExporter(t *testing.T, action func(testWithExporterParams)) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	manager, err := NewManager(config.MetricsConfig{}, time.Millisecond*10, mockLog.Loggers)
	require.NoError(t, err)
	defer manager.Close()

	// Since the global OpenCensus state will accumulate metrics from different tests, we'll use a randomized
	// environment name to isolate the data from this particular test.
	envName := "env-" + uuid.New()

	env, err := manager.AddEnvironment(envName, nil)
	require.NoError(t, err)

	exporter := st.NewTestMetricsExporter()
	exporter.WithExporter(func() {
		action(testWithExporterParams{
			exporter: exporter,
			relayID:  manager.metricsRelayID,
			envName:  envName,
			env:      env,
			mockLog:  mockLog,
		})
	})
}

type testExporterTypeImpl struct {
	name            string
	checkEnabled    func(config.MetricsConfig) bool
	errorOnCreate   error
	errorOnRegister error
	errorOnClose    error
	created         []*testExporterImpl
}

type testExporterImpl struct {
	exporterType *testExporterTypeImpl
	registered   bool
	closed       bool
}

func (t *testExporterTypeImpl) getName() string {
	if t.name == "" {
		return "testExporter"
	}
	return t.name
}

func (t *testExporterTypeImpl) createExporterIfEnabled(
	mc config.MetricsConfig,
	loggers ldlog.Loggers,
) (exporter, error) {
	if t.errorOnCreate != nil {
		return nil, t.errorOnCreate
	}
	if t.checkEnabled != nil && !t.checkEnabled(mc) {
		return nil, nil
	}
	impl := &testExporterImpl{exporterType: t}
	t.created = append(t.created, impl)
	return impl, nil
}

func (t *testExporterImpl) register() error {
	if t.exporterType.errorOnRegister == nil {
		t.registered = true
	}
	return t.exporterType.errorOnRegister
}

func (t *testExporterImpl) close() error {
	if t.exporterType.errorOnClose == nil {
		t.closed = true
	}
	return t.exporterType.errorOnClose
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
func (p *testEventsPublisher) Flush()                                 {}
func (p *testEventsPublisher) Close()                                 {}
func (p *testEventsPublisher) ReplaceCredential(config.SDKCredential) {}

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
