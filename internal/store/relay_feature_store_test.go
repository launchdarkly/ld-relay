package store

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestComponents() (*mockStore, *streamUpdatesStoreWrapper, *mockEnvStreamsUpdates) {
	baseStore := &mockStore{realStore: sharedtest.NewInMemoryStore()}
	updates := &mockEnvStreamsUpdates{}
	store := newStreamUpdatesStoreWrapper(updates, baseStore, ldlog.NewDisabledLoggers())
	return baseStore, store, updates
}

func TestStoreAdapterLazilyCreatesStore(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}
	updates := &mockEnvStreamsUpdates{}

	adapter := NewSSERelayDataStoreAdapter(factory, updates)
	assert.Nil(t, adapter.GetStore())

	context := subsystems.BasicClientContext{}

	created, err := adapter.Build(context)
	require.NoError(t, err)
	require.IsType(t, &streamUpdatesStoreWrapper{}, created)
	assert.Equal(t, context, factory.receivedContext)

	wrappedStore := created.(*streamUpdatesStoreWrapper)
	assert.Equal(t, wrappedStore, adapter.GetStore())
	assert.Equal(t, store, wrappedStore.store)
	assert.Equal(t, updates, wrappedStore.updates)
}

func TestStoreAdapterReturnsErrorIfStoreCannotBeCreated(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}
	factory.fakeError = fakeError
	updates := &mockEnvStreamsUpdates{}

	adapter := NewSSERelayDataStoreAdapter(factory, updates)
	context := subsystems.BasicClientContext{}
	created, err := adapter.Build(context)

	assert.Equal(t, fakeError, err)
	assert.Nil(t, created)
	assert.Nil(t, adapter.GetStore())
}

func TestStoreInit(t *testing.T) {
	baseStore, wrappedStore, updates := makeTestComponents()
	err := wrappedStore.Init(allData)
	assert.NoError(t, err)

	flags, _ := baseStore.GetAll(ldstoreimpl.Features())
	assert.Equal(t, allData[0].Items, flags)

	segments, _ := baseStore.GetAll(ldstoreimpl.Segments())
	assert.Equal(t, allData[1].Items, segments)

	assert.Equal(t,
		allData,
		updates.expectAllDataUpdate(t),
	)
}

func TestStoreGet(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
	_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)

	flag, _ := wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.Equal(t, sharedtest.FlagDesc(testFlag1), flag)

	segment, _ := wrappedStore.Get(ldstoreimpl.Segments(), testSegment1.Key)
	assert.Equal(t, sharedtest.SegmentDesc(testSegment1), segment)
}

func TestStoreGetAll(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
	_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)

	flags, _ := wrappedStore.GetAll(ldstoreimpl.Features())
	expectedFlags, _ := baseStore.GetAll(ldstoreimpl.Features())
	assert.Equal(t, expectedFlags, flags)

	segments, _ := wrappedStore.GetAll(ldstoreimpl.Segments())
	expectedSegments, _ := baseStore.GetAll(ldstoreimpl.Segments())
	assert.Equal(t, expectedSegments, segments)
}

func TestStoreUpsertNewItem(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		_, _ = sharedtest.UpsertFlag(wrappedStore, testFlag1)

		flag, _ := baseStore.Get(ldstoreimpl.Features(), testFlag1.Key)
		assert.Equal(t, sharedtest.FlagDesc(testFlag1), flag)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Features(),
				Key:  testFlag1.Key,
				Item: sharedtest.FlagDesc(testFlag1),
			},
			updates.expectItemUpdate(t),
		)
	})

	t.Run("segment", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		_, _ = sharedtest.UpsertSegment(wrappedStore, testSegment1)

		segment, _ := baseStore.Get(ldstoreimpl.Segments(), testSegment1.Key)
		assert.Equal(t, sharedtest.SegmentDesc(testSegment1), segment)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Segments(),
				Key:  testSegment1.Key,
				Item: sharedtest.SegmentDesc(testSegment1),
			},
			updates.expectItemUpdate(t),
		)
	})
}

func TestStoreUpsertExistingItemWithNewVersion(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
		testFlag1v2 := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 1).Build()
		_, _ = sharedtest.UpsertFlag(store, testFlag1v2)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Features(),
				Key:  testFlag1.Key,
				Item: sharedtest.FlagDesc(testFlag1v2),
			},
			updates.expectItemUpdate(t),
		)
	})

	t.Run("segment", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)
		testSegment1v2 := ldbuilders.NewSegmentBuilder(testSegment1.Key).Version(testSegment1.Version + 1).Build()
		_, _ = sharedtest.UpsertSegment(store, testSegment1v2)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Segments(),
				Key:  testSegment1.Key,
				Item: sharedtest.SegmentDesc(testSegment1v2),
			},
			updates.expectItemUpdate(t),
		)
	})
}

func TestStoreUpsertExistingItemWithOldVersion(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testFlag1v2 := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 1).Build()
		_, _ = sharedtest.UpsertFlag(baseStore, testFlag1v2)
		_, _ = sharedtest.UpsertFlag(store, testFlag1)

		updates.expectItemUpdate(t)
	})

	t.Run("segment", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testSegment1v2 := ldbuilders.NewSegmentBuilder(testSegment1.Key).Version(testSegment1.Version + 1).Build()
		_, _ = sharedtest.UpsertSegment(baseStore, testSegment1v2)
		_, _ = sharedtest.UpsertSegment(store, testSegment1)

		updates.expectItemUpdate(t)
	})
}

func TestStoreDeleteItemWithNewVersion(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
		deletedItem := sharedtest.DeletedItem(testFlag1.Version + 1)
		_, _ = store.Upsert(ldstoreimpl.Features(), testFlag1.Key, deletedItem)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Features(),
				Key:  testFlag1.Key,
				Item: deletedItem,
			},
			updates.expectItemUpdate(t),
		)
	})

	t.Run("segment", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)
		deletedItem := sharedtest.DeletedItem(testSegment1.Version + 1)
		_, _ = store.Upsert(ldstoreimpl.Segments(), testSegment1.Key, deletedItem)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Segments(),
				Key:  testSegment1.Key,
				Item: deletedItem,
			},
			updates.expectItemUpdate(t),
		)
	})
}

func TestStoreDeleteItemWithOlderVersion(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testFlag1v2 := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 1).Build()
		_, _ = sharedtest.UpsertFlag(baseStore, testFlag1v2)
		deletedItem := sharedtest.DeletedItem(testFlag1.Version)
		_, _ = store.Upsert(ldstoreimpl.Features(), testFlag1.Key, deletedItem)

		updates.expectItemUpdate(t)
	})

	t.Run("segment", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testSegment1v2 := ldbuilders.NewSegmentBuilder(testSegment1.Key).Version(testSegment1.Version + 1).Build()
		_, _ = sharedtest.UpsertSegment(baseStore, testSegment1v2)
		deletedItem := sharedtest.DeletedItem(testSegment1.Version)
		_, _ = store.Upsert(ldstoreimpl.Segments(), testSegment1.Key, deletedItem)

		updates.expectItemUpdate(t)
	})
}

func TestUpdatesAreSentEvenIfStoreReturnedError(t *testing.T) {
	t.Run("Init", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		baseStore.fakeError = fakeError
		err := wrappedStore.Init(allData)
		assert.Equal(t, fakeError, err)

		updates.expectAllDataUpdate(t)
	})

	t.Run("Upsert", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		baseStore.fakeError = fakeError
		_, err := sharedtest.UpsertFlag(wrappedStore, testFlag1)
		assert.Equal(t, fakeError, err)

		updates.expectItemUpdate(t)
	})
}

func TestStoreIsInitialized(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	assert.False(t, wrappedStore.IsInitialized())
	_ = baseStore.Init(nil)
	assert.True(t, wrappedStore.IsInitialized())
}

func TestStoreIsStatusMonitoringEnabled(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	assert.False(t, wrappedStore.IsStatusMonitoringEnabled())
	baseStore.statusMonitoring = true
	assert.True(t, wrappedStore.IsStatusMonitoringEnabled())
}

func TestStoreClose(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	wrappedStore.Close()
	assert.True(t, baseStore.closed)
}

func TestSnapshotInitSavesData(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()

	assert.False(t, wrappedStore.HasSnapshot(), "snapshot should not exist before Init")

	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	assert.True(t, wrappedStore.HasSnapshot(), "snapshot should exist after Init with data")

	snapshot := wrappedStore.GetSnapshot()
	require.NotNil(t, snapshot)
	assert.Equal(t, len(allData), len(snapshot))

	for i, coll := range allData {
		assert.Equal(t, coll.Kind, snapshot[i].Kind)
		assert.Equal(t, len(coll.Items), len(snapshot[i].Items))
		for j, item := range coll.Items {
			assert.Equal(t, item.Key, snapshot[i].Items[j].Key)
			assert.Equal(t, item.Item.Version, snapshot[i].Items[j].Item.Version)
		}
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	snapshot1 := wrappedStore.GetSnapshot()
	snapshot2 := wrappedStore.GetSnapshot()

	// Modifying one snapshot should not affect the other
	snapshot1[0].Items = nil
	assert.NotNil(t, snapshot2[0].Items, "snapshots should be independent deep copies")
}

func TestSnapshotEmptyInitDoesNotSetHasData(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init([]ldstoretypes.Collection{})
	require.NoError(t, err)

	assert.False(t, wrappedStore.HasSnapshot(), "snapshot should not be set for empty Init")
}

func TestSnapshotNilInitDoesNotSetHasData(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(nil)
	require.NoError(t, err)

	assert.False(t, wrappedStore.HasSnapshot(), "snapshot should not be set for nil Init")
}

func TestSnapshotInitWithEmptyCollectionsDoesNotSetHasData(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	emptyCollections := []ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	}
	err := wrappedStore.Init(emptyCollections)
	require.NoError(t, err)

	assert.False(t, wrappedStore.HasSnapshot(), "snapshot should not be set when all collections are empty")
}

func TestSnapshotUpsertUpdatesExistingItem(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	testFlag1v2 := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 1).On(false).Build()
	_, _ = sharedtest.UpsertFlag(wrappedStore, testFlag1v2)

	snapshot := wrappedStore.GetSnapshot()
	require.NotNil(t, snapshot)

	var found bool
	for _, coll := range snapshot {
		if coll.Kind == ldstoreimpl.Features() {
			for _, item := range coll.Items {
				if item.Key == testFlag1.Key {
					assert.Equal(t, testFlag1v2.Version, item.Item.Version)
					found = true
				}
			}
		}
	}
	assert.True(t, found, "updated flag should be in snapshot")
}

func TestSnapshotUpsertAddsNewItem(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	_, _ = sharedtest.UpsertFlag(wrappedStore, testFlag2)

	snapshot := wrappedStore.GetSnapshot()
	require.NotNil(t, snapshot)

	var foundFlag1, foundFlag2 bool
	for _, coll := range snapshot {
		if coll.Kind == ldstoreimpl.Features() {
			for _, item := range coll.Items {
				if item.Key == testFlag1.Key {
					foundFlag1 = true
				}
				if item.Key == testFlag2.Key {
					foundFlag2 = true
				}
			}
		}
	}
	assert.True(t, foundFlag1, "original flag should still be in snapshot")
	assert.True(t, foundFlag2, "new flag should be in snapshot")
}

func TestSnapshotUpsertBeforeInitIsIgnored(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()

	_, _ = sharedtest.UpsertFlag(wrappedStore, testFlag1)
	assert.False(t, wrappedStore.HasSnapshot(), "snapshot should not be created by Upsert alone")
}

func TestSnapshotUpsertSegment(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	testSegment1v2 := ldbuilders.NewSegmentBuilder(testSegment1.Key).Version(testSegment1.Version + 1).Build()
	_, _ = sharedtest.UpsertSegment(wrappedStore, testSegment1v2)

	snapshot := wrappedStore.GetSnapshot()
	require.NotNil(t, snapshot)

	var found bool
	for _, coll := range snapshot {
		if coll.Kind == ldstoreimpl.Segments() {
			for _, item := range coll.Items {
				if item.Key == testSegment1.Key {
					assert.Equal(t, testSegment1v2.Version, item.Item.Version)
					found = true
				}
			}
		}
	}
	assert.True(t, found, "updated segment should be in snapshot")
}

func TestSnapshotUpsertIgnoresOlderVersion(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	newerFlag := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 10).On(false).Build()
	_, _ = sharedtest.UpsertFlag(wrappedStore, newerFlag)

	olderFlag := ldbuilders.NewFlagBuilder(testFlag1.Key).Version(testFlag1.Version + 1).On(true).Build()
	_, _ = sharedtest.UpsertFlag(wrappedStore, olderFlag)

	snapshot := wrappedStore.GetSnapshot()
	require.NotNil(t, snapshot)

	for _, coll := range snapshot {
		if coll.Kind == ldstoreimpl.Features() {
			for _, item := range coll.Items {
				if item.Key == testFlag1.Key {
					assert.Equal(t, newerFlag.Version, item.Item.Version,
						"snapshot should retain the newer version, not regress to older")
					return
				}
			}
		}
	}
	t.Fatal("flag not found in snapshot")
}

// Circuit breaker tests

func TestCircuitBreakerOpensOnGetError(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Simulate store failure
	baseStore.fakeError = fakeError

	item, err := wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.NoError(t, err, "should not return error when snapshot is available")
	assert.Equal(t, testFlag1.Version, item.Version)
	assert.True(t, wrappedStore.IsStoreDown(), "circuit breaker should be open")
}

func TestCircuitBreakerOpensOnGetAllError(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	baseStore.fakeError = fakeError

	items, err := wrappedStore.GetAll(ldstoreimpl.Features())
	assert.NoError(t, err, "should not return error when snapshot is available")
	assert.Equal(t, 1, len(items), "should return snapshot data")
	assert.True(t, wrappedStore.IsStoreDown(), "circuit breaker should be open")
}

func TestCircuitBreakerSkipsStoreOnSubsequentReads(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Trigger circuit breaker
	baseStore.fakeError = fakeError
	_, _ = wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.True(t, wrappedStore.IsStoreDown())

	// Subsequent reads should serve from snapshot without touching the store
	item, err := wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.NoError(t, err)
	assert.Equal(t, testFlag1.Version, item.Version)

	items, err := wrappedStore.GetAll(ldstoreimpl.Features())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(items))
}

func TestCircuitBreakerReturnsErrorWithoutSnapshot(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	// No Init called, so no snapshot exists

	baseStore.fakeError = fakeError

	// Without snapshot, errors pass through even with circuit open
	_, err := wrappedStore.Get(ldstoreimpl.Features(), "any-key")
	assert.Equal(t, fakeError, err, "should return error when no snapshot is available")

	_, err = wrappedStore.GetAll(ldstoreimpl.Features())
	assert.Equal(t, fakeError, err, "should return error when no snapshot is available")

	// Circuit breaker should still be set even though we can't serve data
	assert.False(t, wrappedStore.IsStoreDown(), "circuit should not open without snapshot")
}

func TestCircuitBreakerFallsThroughToStoreWithoutSnapshot(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	// No Init, so no snapshot. Set circuit breaker manually.
	wrappedStore.SetStoreDown(true)
	baseStore.fakeError = fakeError

	// With storeDown=true but no snapshot, it should still try the store
	_, err := wrappedStore.Get(ldstoreimpl.Features(), "any-key")
	assert.Equal(t, fakeError, err, "should fall through to store when no snapshot")

	_, err = wrappedStore.GetAll(ldstoreimpl.Features())
	assert.Equal(t, fakeError, err, "should fall through to store when no snapshot")
}

func TestCircuitBreakerClearResumesNormalReads(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Open circuit
	baseStore.fakeError = fakeError
	_, _ = wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.True(t, wrappedStore.IsStoreDown())

	// Clear circuit and fix the store
	baseStore.fakeError = nil
	wrappedStore.SetStoreDown(false)
	assert.False(t, wrappedStore.IsStoreDown())

	// Reads should go to the real store again
	item, err := wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.NoError(t, err)
	assert.Equal(t, testFlag1.Version, item.Version)
}

func TestCircuitBreakerGetReturnsNotFoundFromSnapshot(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Circuit is open, ask for a key that doesn't exist in snapshot
	wrappedStore.SetStoreDown(true)

	item, err := wrappedStore.Get(ldstoreimpl.Features(), "nonexistent-key")
	assert.NoError(t, err)
	assert.Equal(t, ldstoretypes.ItemDescriptor{}.NotFound(), item)
}

func TestCircuitBreakerGetAllReturnsEmptyForUnknownKind(t *testing.T) {
	_, wrappedStore, _ := makeTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	wrappedStore.SetStoreDown(true)

	// Segments exist in snapshot, so this should work
	items, err := wrappedStore.GetAll(ldstoreimpl.Segments())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(items))
}
