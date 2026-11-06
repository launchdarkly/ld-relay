package sdks

import (
	"sync"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// readOnlyAvailabilityKey is the key that the availability check reads. The item does not have to
// exist. A read that succeeds is sufficient.
const readOnlyAvailabilityKey = "$relay-read-only-availability"

// readOnlyPersistentStoreConfigurer builds a persistent store that Relay never writes to.
type readOnlyPersistentStoreConfigurer struct {
	inner   subsystems.ComponentConfigurer[subsystems.PersistentDataStore]
	loggers ldlog.Loggers
}

// newReadOnlyPersistentStore wraps a persistent store configurer so that the store it builds is read-only.
func newReadOnlyPersistentStore(
	inner subsystems.ComponentConfigurer[subsystems.PersistentDataStore],
	loggers ldlog.Loggers,
) subsystems.ComponentConfigurer[subsystems.PersistentDataStore] {
	return readOnlyPersistentStoreConfigurer{inner: inner, loggers: loggers}
}

func (c readOnlyPersistentStoreConfigurer) Build(
	context subsystems.ClientContext,
) (subsystems.PersistentDataStore, error) {
	store, err := c.inner.Build(context)
	if err != nil {
		return nil, err
	}
	return &readOnlyPersistentStore{inner: store, loggers: c.loggers}, nil
}

// DescribeConfiguration gives the description of the wrapped store, so diagnostic events do not change.
func (c readOnlyPersistentStoreConfigurer) DescribeConfiguration(context subsystems.ClientContext) ldvalue.Value {
	if d, ok := c.inner.(subsystems.DiagnosticDescription); ok {
		return d.DescribeConfiguration(context)
	}
	return ldvalue.Null()
}

// readOnlyPersistentStore is a persistent store that Relay reads from but never writes to.
//
// Init and Upsert keep the data in memory instead of writing it. Reads go to the wrapped store, and
// the result is merged with the data in memory: for each item, the higher version wins. The merge
// is necessary because the store gets updates only from other Relay instances. When this instance
// receives an update first, the store can still have the old version. Without the merge, the SDK
// cache would get the old version and keep it for the full cache TTL.
//
// Before the first Init, there is no data in memory, and reads come only from the wrapped store.
// This lets Relay serve the stored data before it connects to LaunchDarkly. After the first Init,
// a failed read returns the data in memory. Relay then does not depend on the store to serve data
// that it already has.
type readOnlyPersistentStore struct {
	inner   subsystems.PersistentDataStore
	loggers ldlog.Loggers

	mu     sync.RWMutex
	items  map[string]map[string]ldstoretypes.SerializedItemDescriptor // kind name -> key -> item
	inited bool
}

// Init replaces the data in memory with a full data set from LaunchDarkly.
//
// A full data set does not contain the items that were deleted. Init adds a tombstone for each
// item that is not in the data set but is in memory or in the store. Without the tombstone, a
// read merges in the copy from the store, and Relay serves a deleted item until the other Relay
// instances update the store. The tombstone has the latest known version of the item, and the
// merge gives equal versions to the data in memory, so the tombstone wins.
func (s *readOnlyPersistentStore) Init(allData []ldstoretypes.SerializedCollection) error {
	// Read the store before the lock is held. Reads can be slow.
	stored := make(map[string]map[string]int, len(allData)) // kind name -> key -> version
	for _, coll := range allData {
		storedItems, err := s.inner.GetAll(coll.Kind)
		if err != nil {
			s.loggers.Warnf("Read-only data store read failed, deleted items are known only from memory: %s", err)
			continue
		}
		versions := make(map[string]int, len(storedItems))
		for _, keyed := range storedItems {
			versions[keyed.Key] = storedVersion(coll.Kind, keyed.Item)
		}
		stored[coll.Kind.GetName()] = versions
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	items := make(map[string]map[string]ldstoretypes.SerializedItemDescriptor, len(allData))
	for _, coll := range allData {
		kindItems := make(map[string]ldstoretypes.SerializedItemDescriptor, len(coll.Items))
		for _, keyed := range coll.Items {
			kindItems[keyed.Key] = keyed.Item
		}
		storedVersions, storeWasRead := stored[coll.Kind.GetName()]
		for key, old := range s.items[coll.Kind.GetName()] {
			if _, ok := kindItems[key]; ok {
				continue
			}
			storedVer, inStore := storedVersions[key]
			if old.Deleted && storeWasRead && !inStore {
				continue // the store does not have the item, so the tombstone is not necessary
			}
			kindItems[key] = tombstone(coll.Kind, max(old.Version, storedVer))
		}
		for key, storedVer := range storedVersions {
			if _, ok := kindItems[key]; !ok {
				kindItems[key] = tombstone(coll.Kind, storedVer)
			}
		}
		items[coll.Kind.GetName()] = kindItems
	}
	s.items = items
	s.inited = true
	return nil
}

func tombstone(kind ldstoretypes.DataKind, version int) ldstoretypes.SerializedItemDescriptor {
	return ldstoretypes.SerializedItemDescriptor{
		Version:        version,
		Deleted:        true,
		SerializedItem: kind.Serialize(ldstoretypes.ItemDescriptor{Version: version}),
	}
}

func (s *readOnlyPersistentStore) Upsert(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.SerializedItemDescriptor,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]map[string]ldstoretypes.SerializedItemDescriptor)
	}
	kindItems := s.items[kind.GetName()]
	if kindItems == nil {
		kindItems = make(map[string]ldstoretypes.SerializedItemDescriptor)
		s.items[kind.GetName()] = kindItems
	}
	if old, ok := kindItems[key]; ok && old.Version >= item.Version {
		return false, nil
	}
	kindItems[key] = item
	return true, nil
}

func (s *readOnlyPersistentStore) Get(
	kind ldstoretypes.DataKind,
	key string,
) (ldstoretypes.SerializedItemDescriptor, error) {
	s.mu.RLock()
	local, hasLocal := s.items[kind.GetName()][key]
	inited := s.inited
	s.mu.RUnlock()

	stored, err := s.inner.Get(kind, key)
	if err != nil {
		if !inited {
			return ldstoretypes.SerializedItemDescriptor{}.NotFound(), err
		}
		s.loggers.Warnf("Read-only data store read failed, serving data received from LaunchDarkly: %s", err)
		if hasLocal {
			return local, nil
		}
		return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
	}
	if hasLocal && local.Version >= storedVersion(kind, stored) {
		return local, nil
	}
	return stored, nil
}

func (s *readOnlyPersistentStore) GetAll(
	kind ldstoretypes.DataKind,
) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	stored, err := s.inner.GetAll(kind)

	s.mu.RLock()
	defer s.mu.RUnlock()
	local := s.items[kind.GetName()]
	if err != nil {
		if !s.inited {
			return nil, err
		}
		s.loggers.Warnf("Read-only data store read failed, serving data received from LaunchDarkly: %s", err)
		stored = nil
	}

	result := make([]ldstoretypes.KeyedSerializedItemDescriptor, 0, len(stored)+len(local))
	storedIsNewer := make(map[string]struct{})
	for _, keyed := range stored {
		l, ok := local[keyed.Key]
		if ok && l.Version >= storedVersion(kind, keyed.Item) {
			continue // the item in memory is added below
		}
		result = append(result, keyed)
		if ok {
			storedIsNewer[keyed.Key] = struct{}{}
		}
	}
	for key, l := range local {
		if _, ok := storedIsNewer[key]; !ok {
			result = append(result, ldstoretypes.KeyedSerializedItemDescriptor{Key: key, Item: l})
		}
	}
	return result, nil
}

func (s *readOnlyPersistentStore) IsInitialized() bool {
	s.mu.RLock()
	inited := s.inited
	s.mu.RUnlock()
	return inited || s.inner.IsInitialized()
}

// IsStoreAvailable does a read, because the availability checks of some stores write. A replica
// refuses those writes.
func (s *readOnlyPersistentStore) IsStoreAvailable() bool {
	_, err := s.inner.Get(ldstoreimpl.Features(), readOnlyAvailabilityKey)
	return err == nil
}

func (s *readOnlyPersistentStore) Close() error {
	return s.inner.Close()
}

// storedVersion gives the version of an item that the wrapped store returned. Some stores do not set
// the version in the descriptor, so the version comes from the serialized item when there is one.
func storedVersion(kind ldstoretypes.DataKind, item ldstoretypes.SerializedItemDescriptor) int {
	if item.SerializedItem == nil {
		return item.Version
	}
	deserialized, err := kind.Deserialize(item.SerializedItem)
	if err != nil {
		return item.Version
	}
	return deserialized.Version
}
