package store

import (
	"sync"

	"github.com/launchdarkly/ld-relay/v6/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
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
	store          subsystems.DataStore
	wrappedFactory subsystems.DataStoreFactory
	updates        streams.EnvStreamUpdates
	mu             sync.RWMutex
}

// DataStoreProvider is an interface implemented by SSERelayDataStoreAdapter, describing a component that
// may or may not yet have a data store.
type DataStoreProvider interface {
	// GetStore returns the current data store, or nil if it has not been created.
	GetStore() subsystems.DataStore
}

// GetStore returns the current data store, or nil if it has not been created.
func (a *SSERelayDataStoreAdapter) GetStore() subsystems.DataStore {
	a.mu.RLock()
	store := a.store
	a.mu.RUnlock()
	return store
}

// GetUpdates returns the EnvStreamUpdates that will receive all updates sent to this store. This is
// exposed for testing so that we can simulate receiving updates from LaunchDarkly to this component.
func (a *SSERelayDataStoreAdapter) GetUpdates() streams.EnvStreamUpdates {
	a.mu.RLock()
	updates := a.updates
	a.mu.RUnlock()
	return updates
}

// NewSSERelayDataStoreAdapter creates a new instance where the store has not yet been created.
func NewSSERelayDataStoreAdapter(
	wrappedFactory subsystems.DataStoreFactory,
	updates streams.EnvStreamUpdates,
) *SSERelayDataStoreAdapter {
	return &SSERelayDataStoreAdapter{
		wrappedFactory: wrappedFactory,
		updates:        updates,
	}
}

// CreateDataStore is called by the SDK when the LDClient is being created.
func (a *SSERelayDataStoreAdapter) CreateDataStore(
	context subsystems.ClientContext,
	dataStoreUpdates subsystems.DataStoreUpdates,
) (subsystems.DataStore, error) {
	var sw *streamUpdatesStoreWrapper
	wrappedStore, err := a.wrappedFactory.CreateDataStore(context, dataStoreUpdates)
	if err != nil {
		return nil, err // this will cause client initialization to fail immediately
	}
	sw = newStreamUpdatesStoreWrapper(
		a.updates,
		wrappedStore,
		context.GetLogging().Loggers,
	)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.store = sw
	return sw, nil
}

// A DataStore implementation that delegates to an underlying store but also publish
// but also publishes stream updates when the store is modified.
type streamUpdatesStoreWrapper struct {
	store   subsystems.DataStore
	updates streams.EnvStreamUpdates
	loggers ldlog.Loggers
}

func newStreamUpdatesStoreWrapper(
	updates streams.EnvStreamUpdates,
	baseFeatureStore subsystems.DataStore,
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

	// See comments in Upsert for why we call SendAllDataUpdate here even if Init returned an error.
	sw.updates.SendAllDataUpdate(allData)

	return err
}

func (sw *streamUpdatesStoreWrapper) Upsert(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) (bool, error) {
	sw.loggers.Debugf(`Received feature flag update: %s (version %d)`, key, item.Version)
	updated, err := sw.store.Upsert(kind, key, item)

	// Note that Upsert returns two values; the first is a boolean which is true if it really did the update,
	// or false if it did not because the store already contained an equal or greater version number.
	//
	// Now we'll pass the update along to the channel that will broadcast it to any currently connected
	// clients-- regardless of whether the data store was really updated. The rationale is that there could
	// be multiple Relay instances sharing a database, in which case it is normal for one of them to get in
	// first and update the store, and the others will then see that the version number is already updated
	// and therefore not update the store. Any clients connected to those other Relay instances still need
	// to be notified that LD sent out an update.
	//
	// It's also possible for LD to broadcast updates out of order, so that a lower version number is sent
	// after a higher one. In that case, none of the Relay instances will update the database (that's what
	// the version numbers are for, to avoid overwriting fresher data). But we will still send the update
	// along to the clients, because it's not easy for Relay to detect this condition (Upsert returns the
	// same false value as it would for an equal version), and the SDKs already have their own similar logic
	// so they will not apply an out-of-order update.
	//
	// Similarly, even if Relay's data store updated failed (err != nil), we should still notify any
	// connected clients, because they may be using the stream rather than the database as their source of
	// truth.

	sw.updates.SendSingleItemUpdate(kind, key, item)

	return updated, err
}

func (sw *streamUpdatesStoreWrapper) IsInitialized() bool {
	return sw.store.IsInitialized()
}
