package metrics

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/pborman/uuid"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

var errAddEnvironmentAfterClosed = errors.New("tried to add new environment after closing metrics.Manager")

// Manager is the top-level object that controls all of our metrics exporter activity. It should be
// created and retained by the Relay instance, and closed when the Relay instance is closed.
type Manager struct {
	metricsRelayID string
	instruments    *Instruments
	meterProvider  *sdkmetric.MeterProvider
	flushInterval  time.Duration
	logger         *slog.Logger
	closeOnce      sync.Once
	closed         bool
	lock           sync.Mutex
	environments   []*EnvironmentManager
	unscopedEnv    *EnvironmentManager

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
	logger *slog.Logger,
) (*Manager, error) {
	metricsRelayID := uuid.New()

	res := tracing.NewResource(logger)

	var meterProvider *sdkmetric.MeterProvider
	var meter otelmetric.Meter
	if otlpConfig.Enabled {
		opts, err := newOTLPExporters(otlpConfig, logger)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithResource(res))
		opts = append(opts, sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: requestDurationMeasureName},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{MaxSize: 160, MaxScale: 20}},
		)))
		meterProvider = sdkmetric.NewMeterProvider(opts...)
		meter = meterProvider.Meter("ld-relay")
		if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
			logger.Warn("failed to start Go runtime metrics", "error", err)
		}
	} else {
		meter = noop.Meter{}
	}

	connections, _ := meter.Int64UpDownCounter(connMeasureName,
		otelmetric.WithDescription("Number of active HTTP server requests"),
		otelmetric.WithUnit("{request}"))
	requestDuration, _ := meter.Float64Histogram(requestDurationMeasureName,
		otelmetric.WithDescription("Duration of HTTP server requests"),
		otelmetric.WithUnit("s"))
	eventsReceivedBytes, _ := meter.Int64Counter(eventsReceivedMeasureName,
		otelmetric.WithDescription("Bytes of event data received"),
		otelmetric.WithUnit("By"))

	eventsDropped, _ := meter.Int64Counter(eventsDroppedMeasureName,
		otelmetric.WithDescription("Events dropped due to capacity overflow"),
		otelmetric.WithUnit("{event}"))
	eventsSent, _ := meter.Int64Counter(eventsSentMeasureName,
		otelmetric.WithDescription("Events successfully sent"),
		otelmetric.WithUnit("{event}"))
	eventsFailedSend, _ := meter.Int64Counter(eventsSendErrorsMeasureName,
		otelmetric.WithDescription("Events that failed to send after all retries"),
		otelmetric.WithUnit("{event}"))
	eventsBytesSent, _ := meter.Int64Counter(eventsSentSizeMeasureName,
		otelmetric.WithDescription("Bytes of event payloads successfully sent"),
		otelmetric.WithUnit("By"))
	pendingEvents, _ := meter.Int64Gauge(eventsPendingMeasureName,
		otelmetric.WithDescription("Events buffered in the queue"),
		otelmetric.WithUnit("{event}"))

	instruments := &Instruments{
		connections:         connections,
		requestDuration:     requestDuration,
		eventsReceivedBytes: eventsReceivedBytes,
		eventsDropped:       eventsDropped,
		eventsSent:          eventsSent,
		eventsFailedSend:    eventsFailedSend,
		eventsBytesSent:     eventsBytesSent,
		pendingEvents:       pendingEvents,
	}

	usageChan := make(chan any, 256)
	m := &Manager{
		metricsRelayID:       metricsRelayID,
		instruments:          instruments,
		meterProvider:        meterProvider,
		flushInterval:        flushInterval,
		logger:               logger,
		usageChan:            usageChan,
		environmentsForUsage: make(map[string]*environmentMetricUsage),
		unscopedEnv: &EnvironmentManager{
			envKVs: []attribute.KeyValue{
				relayIDAttrKey.String(metricsRelayID),
				envNameAttrKey.String(sanitizeTagValue("")),
			},
		},
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

// GetUnscopedEnvironment returns an EnvironmentManager for recording metrics on requests that are not
// associated with any LD environment, such as the status endpoints and requests that matched no route.
// Its environment.name attribute is the not-provided sentinel, and it has no collector, since there is
// no environment to report usage data for.
func (m *Manager) GetUnscopedEnvironment() *EnvironmentManager {
	return m.unscopedEnv
}

// SetInstrumentsForTest replaces the instruments on this Manager. Intended for testing only.
func (m *Manager) SetInstrumentsForTest(instruments *Instruments) {
	m.instruments = instruments
}

func (m *Manager) UsageActivityCountMessage(envName, userAgent, platformCategory, instanceID, tagsHeader string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindCount, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID, tagsHeader: tagsHeader,
	}
}

func (m *Manager) UsageActivityStreamConnected(envName, userAgent, platformCategory, instanceID, tagsHeader string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindStreamConnected, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID, tagsHeader: tagsHeader,
	}
}

func (m *Manager) UsageActivityStreamDisconnected(envName, userAgent, platformCategory, instanceID, tagsHeader string) {
	m.usageChan <- &usageActivityMessage{
		kind: UsageActivityKindStreamDisconnected, envName: envName, userAgent: userAgent, platformCategory: platformCategory, instanceID: instanceID, tagsHeader: tagsHeader,
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
		collector = newRelayMetricsCollector(m.metricsRelayID, envName, publisher, m.flushInterval, m.logger)
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

// NewEventMetricsRecorder creates an EventMetricsRecorder that records event processing metrics
// with this environment's attributes. The returned recorder satisfies the EventMetrics interfaces
// defined in both the events package and go-sdk-events.
//
// The recorder makes a private copy of the environment attributes to avoid data races with
// attribute.NewSet's in-place sort.
func (em *EnvironmentManager) NewEventMetricsRecorder(instruments *Instruments) *EventMetricsRecorder {
	envKVsCopy := make([]attribute.KeyValue, len(em.envKVs))
	copy(envKVsCopy, em.envKVs)
	return &EventMetricsRecorder{
		instruments: instruments,
		envKVs:      envKVsCopy,
		envAttrs:    attribute.NewSet(envKVsCopy...),
	}
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
