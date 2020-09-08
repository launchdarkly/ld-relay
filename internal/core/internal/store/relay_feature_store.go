package store

import (
	"sync"

	"github.com/launchdarkly/ld-relay/v6/internal/core/streams"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
)

// SSERelayDataStoreAdapter is used to create the data store wrapper that manages updates. When data is
// updated in the underlying store, it calls methods of EnvStreams to broadcast the updates.
//
// Because the SDK normally wants to manage the lifecycle of its components, it requires you to provide
// a factory for any custom component, rather than an instance of the component itself. Then it asks the
// factory to create the instance when the LDClient is created. However, in this case we want to be able
// to access the instance externally.
//
// Also, since streamUpdatesStoreWrapper is a wrapper for an underlying data store that could be a database,
// we need to be able to specify which data store implementation is being used - also as a factory.
//
// So, this factory implementation - which should only be used for a single client at a time - calls the
// wrapped factory to produce the underlying data store, then creates our own store instance, and then
// puts a reference to that instance inside itself where we can see it.
type SSERelayDataStoreAdapter struct {
	store          interfaces.DataStore
	wrappedFactory interfaces.DataStoreFactory
	updates        streams.EnvStreamUpdates
	mu             sync.RWMutex
}

// DataStoreProvider is an interface implemented by SSERelayDataStoreAdapter, describing a component that
// may or may not yet have a data store.
type DataStoreProvider interface {
	// GetStore returns the current data store, or nil if it has not been created.
	GetStore() interfaces.DataStore
}

// GetStore returns the current data store, or nil if it has not been created.
func (a *SSERelayDataStoreAdapter) GetStore() interfaces.DataStore {
	a.mu.RLock()
	store := a.store
	a.mu.RUnlock()
	return store
}

// NewSSERelayDataStoreAdapter creates a new instance where the store has not yet been created.
func NewSSERelayDataStoreAdapter(
	wrappedFactory interfaces.DataStoreFactory,
	updates streams.EnvStreamUpdates,
) *SSERelayDataStoreAdapter {
	return &SSERelayDataStoreAdapter{
		wrappedFactory: wrappedFactory,
		updates:        updates,
	}
}

// CreateDataStore is called by the SDK when the LDClient is being created.
func (a *SSERelayDataStoreAdapter) CreateDataStore(
	context interfaces.ClientContext,
	dataStoreUpdates interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	var sw *streamUpdatesStoreWrapper
	wrappedStore, err := a.wrappedFactory.CreateDataStore(context, dataStoreUpdates)
	if err != nil {
		return nil, err // this will cause client initialization to fail immediately
	}
	sw = newStreamUpdatesStoreWrapper(
		a.updates,
		wrappedStore,
		context.GetLogging().GetLoggers(),
	)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.store = sw
	return sw, nil
}

// A DataStore implementation that delegates to an underlying store but also publish
// but also publishes stream updates when the store is modified.
type streamUpdatesStoreWrapper struct {
	store   interfaces.DataStore
	updates streams.EnvStreamUpdates
	loggers ldlog.Loggers
}

func newStreamUpdatesStoreWrapper(
	updates streams.EnvStreamUpdates,
	baseFeatureStore interfaces.DataStore,
	loggers ldlog.Loggers,
) *streamUpdatesStoreWrapper {
	relayStore := &streamUpdatesStoreWrapper{
		store:   baseFeatureStore,
		updates: updates,
		loggers: loggers,
	}
	return relayStore
}

func (sw *streamUpdatesStoreWrapper) Close() error {
	return sw.store.Close()
}

func (sw *streamUpdatesStoreWrapper) IsStatusMonitoringEnabled() bool {
	return sw.store.IsStatusMonitoringEnabled()
}

func (sw *streamUpdatesStoreWrapper) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	return sw.store.Get(kind, key)
}

func (sw *streamUpdatesStoreWrapper) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	return sw.store.GetAll(kind)
}

func (sw *streamUpdatesStoreWrapper) Init(allData []ldstoretypes.Collection) error {
	sw.loggers.Debug("Received all feature flags")
	err := sw.store.Init(allData)

	if err != nil {
		return err
	}

	sw.updates.SendAllDataUpdate(allData)

	return nil
}

func (sw *streamUpdatesStoreWrapper) Upsert(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) (bool, error) {
	sw.loggers.Debugf(`Received feature flag update: %s (version %d)`, key, item.Version)
	updated, err := sw.store.Upsert(kind, key, item)

	if err != nil {
		return false, err
	}

	// If updated is false, it means that there was already a higher-versioned item in the store
	// so no update was done.
	if updated {
		sw.updates.SendSingleItemUpdate(kind, key, item)
	}

	return updated, nil
}

func (sw *streamUpdatesStoreWrapper) IsInitialized() bool {
	return sw.store.IsInitialized()
}
