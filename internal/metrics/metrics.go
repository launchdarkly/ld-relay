package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/events"

	"github.com/launchdarkly/go-sdk-common/v4/ldlog"

	"github.com/pborman/uuid"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

var (
	errAddEnvironmentAfterClosed = errors.New("tried to add new environment after closing metrics.Manager")
)

func errInitMetricsViews(err error) error { // COVERAGE: can't happen in unit tests (and should never happen at all)
	return fmt.Errorf("error registering metrics views: %w", err)
}

// Manager is the top-level object that controls all of our metrics exporter activity. It should be
// created and retained by the Relay instance, and closed when the Relay instance is closed.
type Manager struct {
	openCensusCtx  context.Context
	metricsRelayID string
	exporters      exportersSet
	environments   []*EnvironmentManager
	flushInterval  time.Duration
	loggers        ldlog.Loggers
	closeOnce      sync.Once
	closed         bool
	lock           sync.Mutex

	usageChan            chan interface{}
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
	openCensusCtx  context.Context
	eventsExporter *openCensusEventsExporter
	closeOnce      sync.Once
}

// NewManager creates a Manager instance.
func NewManager(
	metricsConfig config.MetricsConfig,
	flushInterval time.Duration,
	loggers ldlog.Loggers,
) (*Manager, error) {
	metricsRelayID := uuid.New()

	exporters, err := registerExporters(allExporterTypes(), metricsConfig, loggers)
	if err != nil { // COVERAGE: can't make this happen in unit tests
		return nil, err
	}

	registerPublicViewsOnce.Do(func() {
		err = view.Register(getPublicViews()...)
	})
	if err != nil { // COVERAGE: can't make this happen in unit tests
		return nil, errInitMetricsViews(err)
	}
	registerPrivateViewsOnce.Do(func() {
		err = view.Register(getPrivateViews()...)
	})
	if err != nil { // COVERAGE: can't make this happen in unit tests
		return nil, errInitMetricsViews(err)
	}

	ctx, _ := tag.New(context.Background(), tag.Insert(relayIDTagKey, metricsRelayID))

	usageChan := make(chan interface{})
	m := &Manager{
		openCensusCtx:        ctx,
		metricsRelayID:       metricsRelayID,
		exporters:            exporters,
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
		exporters := m.exporters
		environments := m.environments
		m.exporters = nil
		m.environments = nil
		m.closed = true
		m.lock.Unlock()

		closeExporters(exporters, m.loggers)
		for _, env := range environments {
			env.close()
		}
	})
}

// AddEnvironment creates a new EnvironmentManager with its own OpenCensus context that includes
// a tag for the environment name, and registers its exporter.
func (m *Manager) AddEnvironment(envName string, publisher events.EventPublisher) (*EnvironmentManager, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return nil, errAddEnvironmentAfterClosed
	}

	ctx, _ := tag.New(m.openCensusCtx, tag.Insert(envNameTagKey, sanitizeTagValue(envName)))

	var eventsExporter *openCensusEventsExporter
	if publisher != nil {
		eventsExporter = newOpenCensusEventsExporter(m.metricsRelayID, envName, publisher, m.flushInterval)
		view.RegisterExporter(eventsExporter)
	}

	em := &EnvironmentManager{
		openCensusCtx:  ctx,
		eventsExporter: eventsExporter,
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

// GetOpenCensusContext returns the Context for this EnvironmentManager's OpenCensus operations.
func (em *EnvironmentManager) GetOpenCensusContext() context.Context {
	return em.openCensusCtx
}

// FlushEventsExporter is used in testing to trigger the events exporter to post data to the event publisher.
func (em *EnvironmentManager) FlushEventsExporter() {
	if em.eventsExporter != nil {
		em.eventsExporter.flush()
	}
}

func (em *EnvironmentManager) close() {
	em.closeOnce.Do(func() {
		if em.eventsExporter != nil {
			view.UnregisterExporter(em.eventsExporter)
			em.eventsExporter.close()
		}
	})
}

// sanitizeTagValue ensures tag values are valid for OpenCensus metrics.
// OpenCensus drops empty tag values, which causes cardinality mismatches in views.
// We use descriptive default values instead of generic placeholders.
func sanitizeTagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not-provided"
	}
	return strings.ReplaceAll(v, "/", "_")
}
