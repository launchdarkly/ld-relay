package store

import (
	"sync"

	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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
	wrappedFactory subsystems.ComponentConfigurer[subsystems.DataStore]
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

// GetSnapshotStore returns the current data store as a SnapshotStore (for health check use),
// or nil if the store has not been created.
func (a *SSERelayDataStoreAdapter) GetSnapshotStore() SnapshotStore {
	a.mu.RLock()
	s := a.store
	a.mu.RUnlock()
	if s == nil {
		return nil
	}
	if ss, ok := s.(SnapshotStore); ok {
		return ss
	}
	return nil
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
	wrappedFactory subsystems.ComponentConfigurer[subsystems.DataStore],
	updates streams.EnvStreamUpdates,
) *SSERelayDataStoreAdapter {
	return &SSERelayDataStoreAdapter{
		wrappedFactory: wrappedFactory,
		updates:        updates,
	}
}

// Build is called by the SDK when the LDClient is being created.
func (a *SSERelayDataStoreAdapter) Build(
	context subsystems.ClientContext,
) (subsystems.DataStore, error) {
	var sw *streamUpdatesStoreWrapper
	wrappedStore, err := a.wrappedFactory.Build(context)
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

// A DataStore implementation that delegates to an underlying store
// but also publishes stream updates when the store is modified.
// It also maintains an in-memory snapshot of the latest dataset for resilience
// against data store failures (e.g., Redis restart causing data loss).
type streamUpdatesStoreWrapper struct {
	store   subsystems.DataStore
	updates streams.EnvStreamUpdates
	loggers ldlog.Loggers

	snapshotMu      sync.RWMutex
	snapshot        []ldstoretypes.Collection
	snapshotHasData bool

	storeDownMu sync.RWMutex
	storeDown   bool
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

// HasSnapshot returns true if the wrapper has a valid snapshot with data.
func (sw *streamUpdatesStoreWrapper) HasSnapshot() bool {
	sw.snapshotMu.RLock()
	defer sw.snapshotMu.RUnlock()
	return sw.snapshotHasData
}

// GetSnapshot returns a deep copy of the current snapshot, or nil if none exists.
func (sw *streamUpdatesStoreWrapper) GetSnapshot() []ldstoretypes.Collection {
	sw.snapshotMu.RLock()
	defer sw.snapshotMu.RUnlock()
	if !sw.snapshotHasData {
		return nil
	}
	return deepCopyCollections(sw.snapshot)
}

func (sw *streamUpdatesStoreWrapper) saveSnapshot(allData []ldstoretypes.Collection) {
	hasData := false
	for _, coll := range allData {
		if len(coll.Items) > 0 {
			hasData = true
			break
		}
	}

	sw.snapshotMu.Lock()
	defer sw.snapshotMu.Unlock()
	if hasData {
		sw.snapshot = deepCopyCollections(allData)
		sw.snapshotHasData = true
	} else {
		sw.snapshot = nil
		sw.snapshotHasData = false
	}
}

func (sw *streamUpdatesStoreWrapper) updateSnapshotItem(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) {
	sw.snapshotMu.Lock()
	defer sw.snapshotMu.Unlock()
	if !sw.snapshotHasData {
		return
	}

	for i, coll := range sw.snapshot {
		if coll.Kind.GetName() == kind.GetName() {
			for j, existing := range coll.Items {
				if existing.Key == key {
					if item.Version >= existing.Item.Version {
						sw.snapshot[i].Items[j] = ldstoretypes.KeyedItemDescriptor{
							Key:  key,
							Item: item,
						}
					}
					return
				}
			}
			sw.snapshot[i].Items = append(sw.snapshot[i].Items, ldstoretypes.KeyedItemDescriptor{
				Key:  key,
				Item: item,
			})
			return
		}
	}

	sw.snapshot = append(sw.snapshot, ldstoretypes.Collection{
		Kind: kind,
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: key, Item: item},
		},
	})
}

func deepCopyCollections(src []ldstoretypes.Collection) []ldstoretypes.Collection {
	if src == nil {
		return nil
	}
	dst := make([]ldstoretypes.Collection, len(src))
	for i, coll := range src {
		items := make([]ldstoretypes.KeyedItemDescriptor, len(coll.Items))
		copy(items, coll.Items)
		dst[i] = ldstoretypes.Collection{
			Kind:  coll.Kind,
			Items: items,
		}
	}
	return dst
}

func (sw *streamUpdatesStoreWrapper) Close() error {
	return sw.store.Close()
}

func (sw *streamUpdatesStoreWrapper) IsStatusMonitoringEnabled() bool {
	return sw.store.IsStatusMonitoringEnabled()
}

// IsStoreDown returns true if the circuit breaker is open (store is considered unavailable).
func (sw *streamUpdatesStoreWrapper) IsStoreDown() bool {
	sw.storeDownMu.RLock()
	defer sw.storeDownMu.RUnlock()
	return sw.storeDown
}

// SetStoreDown sets or clears the circuit breaker state.
func (sw *streamUpdatesStoreWrapper) SetStoreDown(down bool) {
	sw.storeDownMu.Lock()
	defer sw.storeDownMu.Unlock()
	sw.storeDown = down
}

func (sw *streamUpdatesStoreWrapper) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	if sw.IsStoreDown() && sw.HasSnapshot() {
		return sw.getFromSnapshot(kind, key), nil
	}

	item, err := sw.store.Get(kind, key)
	if err != nil {
		if sw.HasSnapshot() {
			sw.openCircuitBreaker()
			return sw.getFromSnapshot(kind, key), nil
		}
		return item, err
	}
	return item, nil
}

func (sw *streamUpdatesStoreWrapper) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	if sw.IsStoreDown() && sw.HasSnapshot() {
		return sw.getAllFromSnapshot(kind), nil
	}

	items, err := sw.store.GetAll(kind)
	if err != nil {
		if sw.HasSnapshot() {
			sw.openCircuitBreaker()
			return sw.getAllFromSnapshot(kind), nil
		}
		return nil, err
	}
	return items, nil
}

func (sw *streamUpdatesStoreWrapper) openCircuitBreaker() {
	sw.storeDownMu.Lock()
	alreadyDown := sw.storeDown
	sw.storeDown = true
	sw.storeDownMu.Unlock()
	if !alreadyDown {
		sw.loggers.Warn("Data store read error, activating circuit breaker and serving from in-memory snapshot")
	}
}

func (sw *streamUpdatesStoreWrapper) getFromSnapshot(kind ldstoretypes.DataKind, key string) ldstoretypes.ItemDescriptor {
	sw.snapshotMu.RLock()
	defer sw.snapshotMu.RUnlock()
	for _, coll := range sw.snapshot {
		if coll.Kind.GetName() == kind.GetName() {
			for _, item := range coll.Items {
				if item.Key == key {
					return item.Item
				}
			}
		}
	}
	return ldstoretypes.ItemDescriptor{}.NotFound()
}

func (sw *streamUpdatesStoreWrapper) getAllFromSnapshot(kind ldstoretypes.DataKind) []ldstoretypes.KeyedItemDescriptor {
	sw.snapshotMu.RLock()
	defer sw.snapshotMu.RUnlock()
	for _, coll := range sw.snapshot {
		if coll.Kind.GetName() == kind.GetName() {
			items := make([]ldstoretypes.KeyedItemDescriptor, len(coll.Items))
			copy(items, coll.Items)
			return items
		}
	}
	return nil
}

func (sw *streamUpdatesStoreWrapper) Init(allData []ldstoretypes.Collection) error {
	sw.loggers.Debug("Received all feature flags")
	err := sw.store.Init(allData)

	sw.saveSnapshot(allData)

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

	sw.updateSnapshotItem(kind, key, item)

	sw.updates.SendSingleItemUpdate(kind, key, item)

	return updated, err
}

func (sw *streamUpdatesStoreWrapper) IsInitialized() bool {
	return sw.store.IsInitialized()
}
