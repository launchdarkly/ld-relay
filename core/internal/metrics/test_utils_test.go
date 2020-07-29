package metrics

import (
	"encoding/json"

	"go.opencensus.io/trace"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	testMetricsRelayID = "test-metrics-relay-id"
	userAgentValue     = "my-agent"
)

type testExporter struct {
	spans chan *trace.SpanData
}

func (e *testExporter) ExportSpan(s *trace.SpanData) {
	e.spans <- s
}

func newTestExporter() *testExporter {
	return &testExporter{spans: make(chan *trace.SpanData, 100)}
}

type testExporterTypeImpl struct {
	instance        *testExporter
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
		trace.RegisterExporter(t.exporterType.instance)
		t.registered = true
	}
	return t.exporterType.errorOnRegister
}

func (t *testExporterImpl) close() error {
	if t.exporterType.errorOnClose == nil {
		trace.UnregisterExporter(t.exporterType.instance)
		t.closed = true
	}
	return t.exporterType.errorOnClose
}

type testEventsPublisher struct {
	events chan interface{}
}

func newTestEventsPublisher() *testEventsPublisher {
	return &testEventsPublisher{
		events: make(chan interface{}, 100),
	}
}

func (p *testEventsPublisher) Publish(events ...interface{}) {
	for _, e := range events {
		p.events <- e
	}
}
func (p *testEventsPublisher) PublishRaw(events ...json.RawMessage) {}
func (p *testEventsPublisher) Flush()                               {}
