package metrics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/events"

	"github.com/pborman/uuid"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var errAddEnvironmentAfterClosed = errors.New("tried to add new environment after closing metrics.Manager")

// Manager is the top-level object that controls all of our metrics exporter activity. It should be
// created and retained by the Relay instance, and closed when the Relay instance is closed.
type Manager struct {
	metricsRelayID string
	instruments    *Instruments
	meterProvider  *sdkmetric.MeterProvider
	flushInterval  time.Duration
	loggers        ldlog.Loggers
	closeOnce      sync.Once
	closed         bool
	lock           sync.Mutex
	environments   []*EnvironmentManager

	usageChan            chan any
	environmentsForUsage map[string]*environmentMetricUsage
}

type addEnvironment struct {
	envName   string
	publisher events.EventPublisher
}

type removeEnvironment struct {
	envName string
}

type shutdown struct {
	closed chan struct{}
}

// EnvironmentManager controls the metrics exporter activity for a specific LD environment.
type EnvironmentManager struct {
	envKVs    []attribute.KeyValue
	collector *RelayMetricsCollector
	closeOnce sync.Once
}

// NewManager creates a Manager instance.
func NewManager(
	otlpConfig config.OpenTelemetryConfig,
	flushInterval time.Duration,
	loggers ldlog.Loggers,
) (*Manager, error) {
	metricsRelayID := uuid.New()

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("ld-relay"),
	)

	var meterProvider *sdkmetric.MeterProvider
	var meter otelmetric.Meter
	if otlpConfig.Enabled {
		opts, err := newOTLPExporters(otlpConfig, loggers)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithResource(res))
		meterProvider = sdkmetric.NewMeterProvider(opts...)
		meter = meterProvider.Meter("ld-relay")
	} else {
		meter = noop.Meter{}
	}

	connections, _ := meter.Int64UpDownCounter(connMeasureName,
		otelmetric.WithDescription("current number of connections"))
	newConnections, _ := meter.Int64Counter(newConnMeasureName,
		otelmetric.WithDescription("total number of connections"))
	requests, _ := meter.Int64Counter(requestMeasureName,
		otelmetric.WithDescription("number of hits to a route"))

	instruments := &Instruments{
		connections:    connections,
		newConnections: newConnections,
		requests:       requests,
	}

	usageChan := make(chan any, 256)
	m := &Manager{
		metricsRelayID:       metricsRelayID,
		instruments:          instruments,
		meterProvider:        meterProvider,
		flushInterval:        flushInterval,
		loggers:              loggers,
		usageChan:            usageChan,
		environmentsForUsage: make(map[string]*environmentMetricUsage),
	}
	if m.flushInterval <= 0 {
		m.flushInterval = defaultFlushInterval
	}

	go m.consumeUsageStats()

	return m, nil
}

// GetInstruments returns the OTel instruments for recording metrics.
func (m *Manager) GetInstruments() *Instruments {
	return m.instruments
}

// SetInstrumentsForTest replaces the instruments on this Manager. Intended for testing only.
func (m *Manager) SetInstrumentsForTest(instruments *Instruments) {
	m.instruments = instruments
}

func (m *Manager) UsageActivityCountMessage(envName, userAgent, platformCategory, instanceID string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindCount, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID,
	}
}

func (m *Manager) UsageActivityStreamConnected(envName, userAgent, platformCategory, instanceID string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindStreamConnected, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID,
	}
}

func (m *Manager) UsageActivityStreamDisconnected(envName, userAgent, platformCategory, instanceID string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindStreamDisconnected, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID,
	}
}

func (m *Manager) consumeUsageStats() {
	for usage := range m.usageChan {
		switch usage := usage.(type) {
		case *usageActivityMessage:
			m.forwardUsageStats(usage)
		case addEnvironment:
			if _, ok := m.environmentsForUsage[usage.envName]; !ok {
				em := NewEnvironmentMetricUsage(m.metricsRelayID, usage.publisher, 1*time.Minute)
				m.environmentsForUsage[usage.envName] = em
			}
		case removeEnvironment:
			if em, ok := m.environmentsForUsage[usage.envName]; ok {
				delete(m.environmentsForUsage, usage.envName)
				em.close()
			}
		case shutdown:
			for env, em := range m.environmentsForUsage {
				delete(m.environmentsForUsage, env)
				em.close()
			}
			close(usage.closed)
		}
	}
}

func (m *Manager) forwardUsageStats(usage *usageActivityMessage) {
	if em, ok := m.environmentsForUsage[usage.envName]; ok {
		em.usageActivityMessage(usage)
	}
}

// Close shuts down the Manager and all of its EnvironmentManager instances.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		closed := make(chan struct{})
		m.usageChan <- shutdown{closed: closed}
		<-closed

		m.lock.Lock()
		environments := m.environments
		m.environments = nil
		m.closed = true
		m.lock.Unlock()

		for _, env := range environments {
			env.close()
		}

		if m.meterProvider != nil {
			_ = m.meterProvider.Shutdown(context.Background())
		}
	})
}

// AddEnvironment creates a new EnvironmentManager with its own attribute set that includes
// the environment name.
func (m *Manager) AddEnvironment(envName string, publisher events.EventPublisher) (*EnvironmentManager, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return nil, errAddEnvironmentAfterClosed
	}

	envKVs := []attribute.KeyValue{
		relayIDAttrKey.String(m.metricsRelayID),
		envNameAttrKey.String(sanitizeTagValue(envName)),
	}

	var collector *RelayMetricsCollector
	if publisher != nil {
		collector = newRelayMetricsCollector(m.metricsRelayID, envName, publisher, m.flushInterval, m.loggers)
	}

	em := &EnvironmentManager{
		envKVs:    envKVs,
		collector: collector,
	}
	m.environments = append(m.environments, em)
	return em, nil
}

// AddEnvironmentForUsage informs the Manager to start accepting and tracking usage metrics from our middleware.
func (m *Manager) AddEnvironmentForUsage(envName string, publisher events.EventPublisher) {
	m.usageChan <- addEnvironment{envName: envName, publisher: publisher}
}

// RemoveEnvironment shuts down this EnvironmentManager and removes it from the Manager.
func (m *Manager) RemoveEnvironment(em *EnvironmentManager) {
	m.lock.Lock()
	found := false
	for i, em1 := range m.environments {
		if em1 == em {
			found = true
			m.environments = append(m.environments[:i], m.environments[i+1:]...)
			break
		}
	}
	m.lock.Unlock()

	if found {
		em.close()
	}
}

// RemoveEnvironmentForUsage informs the Manager to stop tracking usage metrics for a particular environment.
func (m *Manager) RemoveEnvironmentForUsage(envName string) {
	m.usageChan <- removeEnvironment{envName: envName}
}

// GetAttributes returns the attribute set for this EnvironmentManager.
func (em *EnvironmentManager) GetAttributes() attribute.Set {
	return attribute.NewSet(em.envKVs...)
}

// FlushEventsExporter is used in testing to trigger the collector to post data to the event publisher.
func (em *EnvironmentManager) FlushEventsExporter() {
	if em.collector != nil {
		em.collector.flush()
	}
}

func (em *EnvironmentManager) close() {
	em.closeOnce.Do(func() {
		if em.collector != nil {
			em.collector.close()
		}
	})
}
