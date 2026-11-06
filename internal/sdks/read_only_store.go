package sdks

import (
	"errors"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	redigo "github.com/gomodule/redigo/redis"
)

// readOnlyAvailabilityKey is the key that the availability check reads. The item does not have to
// exist. A read that succeeds is sufficient.
const readOnlyAvailabilityKey = "$relay-read-only-availability"

// errReadStoppedByInit is the result of a store read that was in progress when the data from
// LaunchDarkly arrived. The SDK does not cache an error.
var errReadStoppedByInit = errors.New("read-only data store read stopped, because data from LaunchDarkly arrived")

// readOnlyDataStoreConfigurer builds the data store that the SDK uses when Relay must not write to the
// database.
type readOnlyDataStoreConfigurer struct {
	// describe gives the description of the database for diagnostic events.
	describe subsystems.ComponentConfigurer[subsystems.PersistentDataStore]
	// persistent gives the configurer of the database store for each Build.
	persistent func() subsystems.ComponentConfigurer[subsystems.PersistentDataStore]
}

// newReadOnlyDataStore gives a data store configurer that reads from the database but never writes to it.
// The persistent function is called for each Build, so that each store can have its own resources.
func newReadOnlyDataStore(
	describe subsystems.ComponentConfigurer[subsystems.PersistentDataStore],
	persistent func() subsystems.ComponentConfigurer[subsystems.PersistentDataStore],
) subsystems.ComponentConfigurer[subsystems.DataStore] {
	return readOnlyDataStoreConfigurer{describe: describe, persistent: persistent}
}

func (c readOnlyDataStoreConfigurer) Build(context subsystems.ClientContext) (subsystems.DataStore, error) {
	inner, err := c.persistent().Build(context)
	if err != nil {
		return nil, err
	}
	core := newReadOnlyPersistentStore(inner)
	// The cache must never expire. After Init, the cache is the only copy of the data from LaunchDarkly.
	store, err := ldcomponents.PersistentDataStore(existingPersistentStore{core}).CacheForever().Build(context)
	if err != nil {
		_ = core.Close()
		return nil, err
	}
	return &readOnlyDataStore{store: store, core: core}, nil
}

// DescribeConfiguration gives the description of the database, so diagnostic events do not change.
func (c readOnlyDataStoreConfigurer) DescribeConfiguration(context subsystems.ClientContext) ldvalue.Value {
	return ldcomponents.PersistentDataStore(c.describe).DescribeConfiguration(context)
}

// existingPersistentStore gives a persistent store that is already built.
type existingPersistentStore struct {
	store subsystems.PersistentDataStore
}

func (e existingPersistentStore) Build(subsystems.ClientContext) (subsystems.PersistentDataStore, error) {
	return e.store, nil
}

// readOnlyDataStore is the data store that the SDK uses in read-only mode. It is above the SDK cache.
//
// The SDK cache keeps the result of each store read. Thus, a read that is in progress when Init occurs
// can put old data from the database into the cache after Init. To prevent this, Init holds the write
// lock while it replaces the cache, and reads hold the read lock.
//
// Only this store gets the data from LaunchDarkly. The SDK also calls the Init of the persistent store
// after a database outage, to restore the database from the cache. Thus, the persistent store cannot
// use its own Init to know that the data from LaunchDarkly is in the cache.
type readOnlyDataStore struct {
	store subsystems.DataStore
	core  *readOnlyPersistentStore
	mu    sync.RWMutex
}

// Init replaces the cache with the data from LaunchDarkly.
func (s *readOnlyDataStore) Init(allData []ldstoretypes.Collection) error {
	// Stop the store reads that are in progress first. Then the reads release the lock at once.
	s.core.setInitialized()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Init(allData)
}

func (s *readOnlyDataStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.Get(kind, key)
}

func (s *readOnlyDataStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.GetAll(kind)
}

// Upsert ignores an item that is not newer than the item in the cache. The persistent store cannot do
// this check, because it keeps no data.
func (s *readOnlyDataStore) Upsert(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) (bool, error) {
	old, err := s.Get(kind, key)
	if err == nil && old.Version >= item.Version {
		return false, nil
	}
	return s.store.Upsert(kind, key, item)
}

func (s *readOnlyDataStore) IsInitialized() bool {
	return s.store.IsInitialized()
}

func (s *readOnlyDataStore) IsStatusMonitoringEnabled() bool {
	return s.store.IsStatusMonitoringEnabled()
}

func (s *readOnlyDataStore) Close() error {
	return s.store.Close()
}

// readOnlyPersistentStore is the persistent store under the SDK cache in read-only mode. It reads from
// the database but never writes to it.
//
// Before the data from LaunchDarkly arrives, reads go to the database. This lets Relay serve the
// stored data before it connects to LaunchDarkly. After that, the cache has every item from
// LaunchDarkly, so a read is only for an item that LaunchDarkly does not have. The database can still
// have that item, for example when another Relay instance has not yet stored a deletion. Thus, these
// reads do not go to the database.
type readOnlyPersistentStore struct {
	inner           subsystems.PersistentDataStore
	initialized     chan struct{} // closed when the data from LaunchDarkly arrives
	initializedOnce sync.Once
}

func newReadOnlyPersistentStore(inner subsystems.PersistentDataStore) *readOnlyPersistentStore {
	return &readOnlyPersistentStore{inner: inner, initialized: make(chan struct{})}
}

// setInitialized records that the data from LaunchDarkly arrived.
func (s *readOnlyPersistentStore) setInitialized() {
	s.initializedOnce.Do(func() { close(s.initialized) })
}

func (s *readOnlyPersistentStore) hasDataFromLaunchDarkly() bool {
	select {
	case <-s.initialized:
		return true
	default:
		return false
	}
}

// Init does not write the data.
func (s *readOnlyPersistentStore) Init([]ldstoretypes.SerializedCollection) error {
	return nil
}

// Upsert does not write the item. It returns true, so that the SDK cache keeps the item.
func (s *readOnlyPersistentStore) Upsert(
	ldstoretypes.DataKind,
	string,
	ldstoretypes.SerializedItemDescriptor,
) (bool, error) {
	return true, nil
}

func (s *readOnlyPersistentStore) Get(
	kind ldstoretypes.DataKind,
	key string,
) (ldstoretypes.SerializedItemDescriptor, error) {
	if s.hasDataFromLaunchDarkly() {
		return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
	}
	return readUntilInitialized(s, func() (ldstoretypes.SerializedItemDescriptor, error) {
		return s.inner.Get(kind, key)
	})
}

func (s *readOnlyPersistentStore) GetAll(
	kind ldstoretypes.DataKind,
) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	if s.hasDataFromLaunchDarkly() {
		return nil, nil
	}
	return readUntilInitialized(s, func() ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
		return s.inner.GetAll(kind)
	})
}

// readUntilInitialized does a database read, but stops when the data from LaunchDarkly arrives. Init
// waits for the reads to complete, and the database can be slow or not respond.
func readUntilInitialized[T any](s *readOnlyPersistentStore, read func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	results := make(chan result, 1)
	go func() {
		value, err := read()
		results <- result{value, err}
	}()
	select {
	case r := <-results:
		return r.value, r.err
	case <-s.initialized:
		var zero T
		return zero, errReadStoppedByInit
	}
}

func (s *readOnlyPersistentStore) IsInitialized() bool {
	return s.hasDataFromLaunchDarkly() || s.inner.IsInitialized()
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

// newReadOnlyRedisPool gives the Redis connection pool for read-only mode. It keeps no idle connections.
// After the data from LaunchDarkly arrives, Relay does not read the database. Idle connections would
// stay open, and they would use connections of the database server.
func newReadOnlyRedisPool(url string, dialOptions []redigo.DialOption) *redigo.Pool {
	return &redigo.Pool{
		MaxIdle:     0,
		MaxActive:   16,
		Wait:        true,
		IdleTimeout: 300 * time.Second,
		Dial: func() (redigo.Conn, error) {
			return redigo.DialURL(url, dialOptions...)
		},
	}
}
