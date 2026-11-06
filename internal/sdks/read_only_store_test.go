package sdks

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// replicaStore is a persistent store in memory. Like the Redis store, it does not set the version in the
// descriptors that it returns.
type replicaStore struct {
	items   map[string]map[string][]byte
	inited  bool
	writes  int
	readErr error
	closed  bool
}

func newReplicaStore() *replicaStore {
	return &replicaStore{items: map[string]map[string][]byte{}}
}

func (f *replicaStore) put(kind ldstoretypes.DataKind, key string, version int) {
	if f.items[kind.GetName()] == nil {
		f.items[kind.GetName()] = map[string][]byte{}
	}
	f.items[kind.GetName()][key] = serializedFlag(key, version).SerializedItem
	f.inited = true
}

func (f *replicaStore) Init([]ldstoretypes.SerializedCollection) error {
	f.writes++
	return nil
}

func (f *replicaStore) Upsert(ldstoretypes.DataKind, string, ldstoretypes.SerializedItemDescriptor) (bool, error) {
	f.writes++
	return true, nil
}

func (f *replicaStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.SerializedItemDescriptor, error) {
	if f.readErr != nil {
		return ldstoretypes.SerializedItemDescriptor{}, f.readErr
	}
	if b, ok := f.items[kind.GetName()][key]; ok {
		return ldstoretypes.SerializedItemDescriptor{SerializedItem: b}, nil
	}
	return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
}

func (f *replicaStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	var result []ldstoretypes.KeyedSerializedItemDescriptor
	for k, b := range f.items[kind.GetName()] {
		result = append(result, ldstoretypes.KeyedSerializedItemDescriptor{
			Key: k, Item: ldstoretypes.SerializedItemDescriptor{SerializedItem: b},
		})
	}
	return result, nil
}

func (f *replicaStore) IsInitialized() bool    { return f.inited }
func (f *replicaStore) IsStoreAvailable() bool { f.writes++; return true } // like Redis atomic mode, it writes
func (f *replicaStore) Close() error           { f.closed = true; return nil }

type replicaStoreConfigurer struct{ store *replicaStore }

func (c replicaStoreConfigurer) Build(subsystems.ClientContext) (subsystems.PersistentDataStore, error) {
	return c.store, nil
}

func serializedFlag(key string, version int) ldstoretypes.SerializedItemDescriptor {
	flag := ldbuilders.NewFlagBuilder(key).Version(version).Build()
	item := ldstoretypes.ItemDescriptor{Version: version, Item: &flag}
	return ldstoretypes.SerializedItemDescriptor{
		Version:        version,
		SerializedItem: ldstoreimpl.Features().Serialize(item),
	}
}

func makeReadOnlyStore(t *testing.T, inner *replicaStore) subsystems.PersistentDataStore {
	store, err := newReadOnlyPersistentStore(replicaStoreConfigurer{inner}).Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	return store
}

func initWith(t *testing.T, store subsystems.PersistentDataStore, flags map[string]int) {
	var items []ldstoretypes.KeyedSerializedItemDescriptor
	for key, version := range flags {
		items = append(items, ldstoretypes.KeyedSerializedItemDescriptor{Key: key, Item: serializedFlag(key, version)})
	}
	require.NoError(t, store.Init([]ldstoretypes.SerializedCollection{{Kind: ldstoreimpl.Features(), Items: items}}))
}

func versionsOf(t *testing.T, items []ldstoretypes.KeyedSerializedItemDescriptor) map[string]int {
	result := map[string]int{}
	for _, keyed := range items {
		item, err := ldstoreimpl.Features().Deserialize(keyed.Item.SerializedItem)
		require.NoError(t, err)
		result[keyed.Key] = item.Version
	}
	return result
}

func TestReadOnlyStoreNeverWrites(t *testing.T) {
	inner := newReplicaStore()
	store := makeReadOnlyStore(t, inner)

	initWith(t, store, map[string]int{"a": 1})
	updated, err := store.Upsert(ldstoreimpl.Features(), "a", serializedFlag("a", 2))
	require.NoError(t, err)
	assert.True(t, updated)
	assert.True(t, store.IsStoreAvailable())

	assert.Equal(t, 0, inner.writes)
}

func TestReadOnlyStoreReadsFromStore(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 3)
	store := makeReadOnlyStore(t, inner)

	assert.True(t, store.IsInitialized())
	all, err := store.GetAll(ldstoreimpl.Features())
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"a": 3}, versionsOf(t, all))

	// Init does not change what the store returns. The SDK cache keeps the data from Init.
	initWith(t, store, map[string]int{"a": 4})
	all, err = store.GetAll(ldstoreimpl.Features())
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"a": 3}, versionsOf(t, all))
}

func TestReadOnlyStoreIsNotInitializedWhenStoreIsEmpty(t *testing.T) {
	store := makeReadOnlyStore(t, newReplicaStore())
	assert.False(t, store.IsInitialized())

	initWith(t, store, map[string]int{"a": 1})
	assert.True(t, store.IsInitialized())
}

func TestReadOnlyStoreReturnsReadErrors(t *testing.T) {
	readErr := errors.New("sorry")
	inner := newReplicaStore()
	store := makeReadOnlyStore(t, inner)
	initWith(t, store, map[string]int{"a": 1})
	inner.readErr = readErr

	_, err := store.GetAll(ldstoreimpl.Features())
	assert.Equal(t, readErr, err)
	_, err = store.Get(ldstoreimpl.Features(), "a")
	assert.Equal(t, readErr, err)
	assert.False(t, store.IsStoreAvailable())
}

func TestReadOnlyStoreCloseClosesStore(t *testing.T) {
	inner := newReplicaStore()
	store := makeReadOnlyStore(t, inner)
	require.NoError(t, store.Close())
	assert.True(t, inner.closed)
}

func makeCachingReadOnlyStore(t *testing.T, inner *replicaStore, ttl time.Duration) subsystems.DataStore {
	ctx := subsystems.BasicClientContext{
		DataStoreUpdateSink: noOpStatusSink{},
		Logging:             subsystems.LoggingConfiguration{Loggers: ldlog.NewDisabledLoggers()},
	}
	store, err := ldcomponents.PersistentDataStore(newReadOnlyPersistentStore(replicaStoreConfigurer{inner})).
		CacheTime(ttl).Build(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The SDK cache serves the data from LaunchDarkly until the cache TTL ends. Then it reads the store.
func TestReadOnlyStoreReadsStoreWhenCacheExpires(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	store := makeCachingReadOnlyStore(t, inner, 100*time.Millisecond)

	require.NoError(t, store.Init([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{{Key: "a", Item: flagItem("a", 2)}}},
		{Kind: ldstoreimpl.Segments()},
	}))
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 2, item.Version)

	inner.put(ldstoreimpl.Features(), "a", 3) // another Relay instance updates the store
	assert.Eventually(t, func() bool {
		item, err := store.Get(ldstoreimpl.Features(), "a")
		return err == nil && item.Version == 3
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, inner.writes)
}

// LaunchDarkly makes the data set before flag "n" exists. Another Relay instance stores "n" before
// this instance gets the data set. Then this instance gets the update that creates "n".
func TestReadOnlyStoreServesItemCreatedWhileConnecting(t *testing.T) {
	for _, ttl := range []time.Duration{0, 30 * time.Second, -1} {
		t.Run(ttl.String(), func(t *testing.T) {
			inner := newReplicaStore()
			inner.put(ldstoreimpl.Features(), "a", 1)
			store := makeCachingReadOnlyStore(t, inner, ttl)

			inner.put(ldstoreimpl.Features(), "n", 1)
			require.NoError(t, store.Init([]ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{{Key: "a", Item: flagItem("a", 1)}}},
				{Kind: ldstoreimpl.Segments()},
			}))
			_, err := store.Upsert(ldstoreimpl.Features(), "n", flagItem("n", 1))
			require.NoError(t, err)

			item, err := store.Get(ldstoreimpl.Features(), "n")
			require.NoError(t, err)
			assert.NotNil(t, item.Item)
			all, err := store.GetAll(ldstoreimpl.Features())
			require.NoError(t, err)
			assert.Len(t, all, 2)
		})
	}
}

type noOpStatusSink struct{}

func (noOpStatusSink) UpdateStatus(interfaces.DataStoreStatus) {}

func flagItem(key string, version int) ldstoretypes.ItemDescriptor {
	flag := ldbuilders.NewFlagBuilder(key).Version(version).Build()
	return ldstoretypes.ItemDescriptor{Version: version, Item: &flag}
}
