package sdks

import (
	"sync/atomic"

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
// Relay requires an infinite cache TTL with this store. Init and Upsert do not change the store, and
// the SDK cache keeps the data from LaunchDarkly. Before the first Init, reads go to the store. This
// lets Relay serve the stored data before it connects to LaunchDarkly. Other Relay instances keep the
// store up to date.
type readOnlyPersistentStore struct {
	inner  subsystems.PersistentDataStore
	inited atomic.Bool
}

// Init does not write the data. It records that Relay has data from LaunchDarkly.
func (s *readOnlyPersistentStore) Init([]ldstoretypes.SerializedCollection) error {
	s.inited.Store(true)
	return nil
}

// Upsert does not write the item. It returns true, so that the SDK cache keeps the item from
// LaunchDarkly.
func (s *readOnlyPersistentStore) Upsert(
	ldstoretypes.DataKind,
	string,
	ldstoretypes.SerializedItemDescriptor,
) (bool, error) {
	return true, nil
}

// Get reads the store only before the first Init. After Init, the SDK cache has every item from
// LaunchDarkly, so the SDK calls Get only for an item that LaunchDarkly does not have. The store can
// still have that item, for example when another Relay instance has not yet stored a deletion. The
// SDK would then cache the item forever.
func (s *readOnlyPersistentStore) Get(
	kind ldstoretypes.DataKind,
	key string,
) (ldstoretypes.SerializedItemDescriptor, error) {
	if s.inited.Load() {
		return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
	}
	return s.inner.Get(kind, key)
}

func (s *readOnlyPersistentStore) GetAll(
	kind ldstoretypes.DataKind,
) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	return s.inner.GetAll(kind)
}

func (s *readOnlyPersistentStore) IsInitialized() bool {
	return s.inited.Load() || s.inner.IsInitialized()
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
