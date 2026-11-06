package sdks

import (
	"sync"

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
	inner subsystems.ComponentConfigurer[subsystems.PersistentDataStore]
}

// newReadOnlyPersistentStore wraps a persistent store configurer so that the store it builds is read-only.
func newReadOnlyPersistentStore(
	inner subsystems.ComponentConfigurer[subsystems.PersistentDataStore],
) subsystems.ComponentConfigurer[subsystems.PersistentDataStore] {
	return readOnlyPersistentStoreConfigurer{inner: inner}
}

func (c readOnlyPersistentStoreConfigurer) Build(
	context subsystems.ClientContext,
) (subsystems.PersistentDataStore, error) {
	store, err := c.inner.Build(context)
	if err != nil {
		return nil, err
	}
	return &readOnlyPersistentStore{inner: store}, nil
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
// Before the first Init, reads go to the wrapped store. This lets Relay serve the stored data before
// it connects to LaunchDarkly. Init and Upsert keep the data in memory instead of writing it. After
// the first Init, reads come only from memory. Relay trusts the data that it gets from LaunchDarkly
// more than the data in the store, which other Relay instances write.
type readOnlyPersistentStore struct {
	inner subsystems.PersistentDataStore

	mu     sync.RWMutex
	items  map[string]map[string]ldstoretypes.SerializedItemDescriptor // kind name -> key -> item
	inited bool
}

// Init replaces the data in memory with a full data set from LaunchDarkly.
func (s *readOnlyPersistentStore) Init(allData []ldstoretypes.SerializedCollection) error {
	items := make(map[string]map[string]ldstoretypes.SerializedItemDescriptor, len(allData))
	for _, coll := range allData {
		kindItems := make(map[string]ldstoretypes.SerializedItemDescriptor, len(coll.Items))
		for _, keyed := range coll.Items {
			kindItems[keyed.Key] = keyed.Item
		}
		items[coll.Kind.GetName()] = kindItems
	}
	s.mu.Lock()
	s.items = items
	s.inited = true
	s.mu.Unlock()
	return nil
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
	inited := s.inited
	item, ok := s.items[kind.GetName()][key]
	s.mu.RUnlock()
	if !inited {
		return s.inner.Get(kind, key)
	}
	if !ok {
		return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
	}
	return item, nil
}

func (s *readOnlyPersistentStore) GetAll(
	kind ldstoretypes.DataKind,
) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	s.mu.RLock()
	if !s.inited {
		s.mu.RUnlock()
		return s.inner.GetAll(kind)
	}
	local := s.items[kind.GetName()]
	result := make([]ldstoretypes.KeyedSerializedItemDescriptor, 0, len(local))
	for key, item := range local {
		result = append(result, ldstoretypes.KeyedSerializedItemDescriptor{Key: key, Item: item})
	}
	s.mu.RUnlock()
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
