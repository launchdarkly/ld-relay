package store

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"

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

	created, err := adapter.CreateDataStore(context, nil)
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
	created, err := adapter.CreateDataStore(context, nil)

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
