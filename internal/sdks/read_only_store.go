package sdks

import (
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	redigo "github.com/gomodule/redigo/redis"
)

// readOnlyAvailabilityKey is the key that the availability check reads. The item does not have to
// exist. A read that succeeds is sufficient.
const readOnlyAvailabilityKey = "$relay-read-only-availability"

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
	sink := readOnlyStatusSink{sink: context.GetDataStoreUpdateSink(), core: core}
	database, err := ldcomponents.PersistentDataStore(existingPersistentStore{core}).
		CacheForever().
		Build(readOnlyClientContext{ClientContext: context, sink: sink})
	if err != nil {
		_ = core.Close()
		return nil, err
	}
	memory, err := ldcomponents.InMemoryDataStore().Build(context)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	return &readOnlyDataStore{database: database, memory: memory, core: core}, nil
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

// readOnlyClientContext gives the database store a status sink of its own.
type readOnlyClientContext struct {
	subsystems.ClientContext
	sink subsystems.DataStoreUpdateSink
}

func (c readOnlyClientContext) GetDataStoreUpdateSink() subsystems.DataStoreUpdateSink {
	return c.sink
}

// readOnlyStatusSink sends the status of the database store to the SDK. After the data from
// LaunchDarkly arrives, Relay does not use the database, so it does not send a database outage.
// It still sends a recovery. The SDK status poller sends the recovery at its next check, because
// the availability check then always succeeds.
type readOnlyStatusSink struct {
	sink subsystems.DataStoreUpdateSink
	core *readOnlyPersistentStore
}

func (s readOnlyStatusSink) UpdateStatus(status interfaces.DataStoreStatus) {
	if !status.Available && s.core.hasDataFromLaunchDarkly() {
		return
	}
	s.sink.UpdateStatus(status)
}

// readOnlyDataStore is the data store that the SDK uses in read-only mode. It has two stores, like the
// read-only mode of the SDK data system:
//
//   - The database store reads the database through the SDK cache. It serves reads until the data
//     from LaunchDarkly arrives. It never writes to the database.
//   - The memory store gets the data from LaunchDarkly and all updates. It serves reads after the
//     data from LaunchDarkly arrives.
//
// Init switches the reads to the memory store after the memory store has the data. A read of the
// database that is in progress at that time cannot change the memory store. No read waits for Init,
// and Init does not wait for a read.
type readOnlyDataStore struct {
	database subsystems.DataStore
	memory   subsystems.DataStore
	core     *readOnlyPersistentStore
}

// Init stores the data from LaunchDarkly in the memory store. Then reads use only the memory store.
func (s *readOnlyDataStore) Init(allData []ldstoretypes.Collection) error {
	if err := s.memory.Init(allData); err != nil {
		return err
	}
	if !s.core.hasDataFromLaunchDarkly() {
		s.core.setInitialized()
		// The cache of the database store is not used again. This releases its memory.
		if c, ok := s.database.(interface{ DropCache() }); ok {
			c.DropCache()
		}
	}
	return nil
}

func (s *readOnlyDataStore) active() subsystems.DataStore {
	if s.core.hasDataFromLaunchDarkly() {
		return s.memory
	}
	return s.database
}

func (s *readOnlyDataStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	return s.active().Get(kind, key)
}

func (s *readOnlyDataStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	return s.active().GetAll(kind)
}

// Upsert updates the memory store. The memory store ignores an item that is not newer than its item.
func (s *readOnlyDataStore) Upsert(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) (bool, error) {
	return s.memory.Upsert(kind, key, item)
}

func (s *readOnlyDataStore) IsInitialized() bool {
	return s.active().IsInitialized()
}

// IsStatusMonitoringEnabled is true, because the database store reports its status.
func (s *readOnlyDataStore) IsStatusMonitoringEnabled() bool {
	return s.database.IsStatusMonitoringEnabled()
}

func (s *readOnlyDataStore) Close() error {
	err := s.database.Close()
	_ = s.memory.Close()
	return err
}

// readOnlyPersistentStore is the persistent store under the SDK cache of the database store. It reads
// from the database but never writes to it.
type readOnlyPersistentStore struct {
	inner           subsystems.PersistentDataStore
	initialized     chan struct{} // closed when the data from LaunchDarkly arrives
	initializedOnce sync.Once
}

func newReadOnlyPersistentStore(inner subsystems.PersistentDataStore) *readOnlyPersistentStore {
	return &readOnlyPersistentStore{inner: inner, initialized: make(chan struct{})}
}

// setInitialized records that the memory store has the data from LaunchDarkly.
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

// Init does not write the data. The SDK calls Init after a database outage, to restore the database
// from the cache.
func (s *readOnlyPersistentStore) Init([]ldstoretypes.SerializedCollection) error {
	return nil
}

// Upsert does not write the item. Relay sends all updates to the memory store, so the SDK does not
// call this.
func (s *readOnlyPersistentStore) Upsert(
	ldstoretypes.DataKind,
	string,
	ldstoretypes.SerializedItemDescriptor,
) (bool, error) {
	return false, nil
}

func (s *readOnlyPersistentStore) Get(
	kind ldstoretypes.DataKind,
	key string,
) (ldstoretypes.SerializedItemDescriptor, error) {
	return s.inner.Get(kind, key)
}

func (s *readOnlyPersistentStore) GetAll(
	kind ldstoretypes.DataKind,
) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	return s.inner.GetAll(kind)
}

func (s *readOnlyPersistentStore) IsInitialized() bool {
	return s.inner.IsInitialized()
}

// IsStoreAvailable does a read, because the availability checks of some stores write. A replica
// refuses those writes. After the data from LaunchDarkly arrives, the result is always true, and the
// check does not wait for the database. Then the SDK status poller stops and reports a recovery.
func (s *readOnlyPersistentStore) IsStoreAvailable() bool {
	if s.hasDataFromLaunchDarkly() {
		return true
	}
	result := make(chan bool, 1)
	go func() {
		_, err := s.inner.Get(ldstoreimpl.Features(), readOnlyAvailabilityKey)
		result <- err == nil
	}()
	select {
	case available := <-result:
		return available
	case <-s.initialized:
		return true
	}
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
