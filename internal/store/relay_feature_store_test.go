package store

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"

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

	overrides, _ := baseStore.GetAll(ldstoreimpl.ConfigOverrides())
	assert.Equal(t, allData[2].Items, overrides)

	metrics, _ := baseStore.GetAll(ldstoreimpl.Metrics())
	assert.Equal(t, allData[3].Items, metrics)

	assert.Equal(t,
		allData,
		updates.expectAllDataUpdate(t),
	)
}

func TestStoreGet(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
	_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)
	_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverride)
	_, _ = sharedtest.UpsertMetric(baseStore, testMetric1)

	flag, _ := wrappedStore.Get(ldstoreimpl.Features(), testFlag1.Key)
	assert.Equal(t, sharedtest.FlagDesc(testFlag1), flag)

	segment, _ := wrappedStore.Get(ldstoreimpl.Segments(), testSegment1.Key)
	assert.Equal(t, sharedtest.SegmentDesc(testSegment1), segment)

	override, _ := wrappedStore.Get(ldstoreimpl.ConfigOverrides(), testIndexSamplingRatioOverride.Key)
	assert.Equal(t, sharedtest.ConfigOverrideDesc(testIndexSamplingRatioOverride), override)

	metric, _ := wrappedStore.Get(ldstoreimpl.Metrics(), testMetric1.Key)
	assert.Equal(t, sharedtest.MetricDesc(testMetric1), metric)
}

func TestStoreGetAll(t *testing.T) {
	baseStore, wrappedStore, _ := makeTestComponents()
	_, _ = sharedtest.UpsertFlag(baseStore, testFlag1)
	_, _ = sharedtest.UpsertSegment(baseStore, testSegment1)
	_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverride)
	_, _ = sharedtest.UpsertMetric(baseStore, testMetric1)

	flags, _ := wrappedStore.GetAll(ldstoreimpl.Features())
	expectedFlags, _ := baseStore.GetAll(ldstoreimpl.Features())
	assert.Equal(t, expectedFlags, flags)

	segments, _ := wrappedStore.GetAll(ldstoreimpl.Segments())
	expectedSegments, _ := baseStore.GetAll(ldstoreimpl.Segments())
	assert.Equal(t, expectedSegments, segments)

	overrides, _ := wrappedStore.GetAll(ldstoreimpl.ConfigOverrides())
	expectedOverrides, _ := baseStore.GetAll(ldstoreimpl.ConfigOverrides())
	assert.Equal(t, expectedOverrides, overrides)

	metrics, _ := wrappedStore.GetAll(ldstoreimpl.Metrics())
	expectedMetrics, _ := baseStore.GetAll(ldstoreimpl.Metrics())
	assert.Equal(t, expectedMetrics, metrics)
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

	t.Run("config override", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		_, _ = sharedtest.UpsertConfigOverride(wrappedStore, testIndexSamplingRatioOverride)

		override, _ := baseStore.Get(ldstoreimpl.ConfigOverrides(), testIndexSamplingRatioOverride.Key)
		assert.Equal(t, sharedtest.ConfigOverrideDesc(testIndexSamplingRatioOverride), override)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.ConfigOverrides(),
				Key:  testIndexSamplingRatioOverride.Key,
				Item: sharedtest.ConfigOverrideDesc(testIndexSamplingRatioOverride),
			},
			updates.expectItemUpdate(t),
		)
	})
	t.Run("metric", func(t *testing.T) {
		baseStore, wrappedStore, updates := makeTestComponents()
		_, _ = sharedtest.UpsertMetric(wrappedStore, testMetric1)

		segment, _ := baseStore.Get(ldstoreimpl.Metrics(), testMetric1.Key)
		assert.Equal(t, sharedtest.MetricDesc(testMetric1), segment)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Metrics(),
				Key:  testMetric1.Key,
				Item: sharedtest.MetricDesc(testMetric1),
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

	t.Run("config override", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverride)
		testIndexSamplingRatioOverridev2 := ldbuilders.NewConfigOverrideBuilder(testIndexSamplingRatioOverride.Key).Version(testIndexSamplingRatioOverride.Version + 1).Build()
		_, _ = sharedtest.UpsertConfigOverride(store, testIndexSamplingRatioOverridev2)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.ConfigOverrides(),
				Key:  testIndexSamplingRatioOverride.Key,
				Item: sharedtest.ConfigOverrideDesc(testIndexSamplingRatioOverridev2),
			},
			updates.expectItemUpdate(t),
		)
	})

	t.Run("metric", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertMetric(baseStore, testMetric1)
		testMetric1v2 := ldbuilders.NewMetricBuilder(testMetric1.Key).Version(testMetric1.Version + 1).Build()
		_, _ = sharedtest.UpsertMetric(store, testMetric1v2)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Metrics(),
				Key:  testMetric1.Key,
				Item: sharedtest.MetricDesc(testMetric1v2),
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

	t.Run("config override", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testIndexSamplingRatioOverridev2 := ldbuilders.NewConfigOverrideBuilder(testIndexSamplingRatioOverride.Key).Version(testSegment1.Version + 1).Build()
		_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverridev2)
		_, _ = sharedtest.UpsertConfigOverride(store, testIndexSamplingRatioOverride)

		updates.expectItemUpdate(t)
	})

	t.Run("metric", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testMetric1v2 := ldbuilders.NewMetricBuilder(testMetric1.Key).Version(testMetric1.Version + 1).Build()
		_, _ = sharedtest.UpsertMetric(baseStore, testMetric1v2)
		_, _ = sharedtest.UpsertMetric(store, testMetric1)

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

	t.Run("config override", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverride)
		deletedItem := sharedtest.DeletedItem(testIndexSamplingRatioOverride.Version + 1)
		_, _ = store.Upsert(ldstoreimpl.ConfigOverrides(), testIndexSamplingRatioOverride.Key, deletedItem)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.ConfigOverrides(),
				Key:  testIndexSamplingRatioOverride.Key,
				Item: deletedItem,
			},
			updates.expectItemUpdate(t),
		)
	})

	t.Run("metric", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		_, _ = sharedtest.UpsertMetric(baseStore, testMetric1)
		deletedItem := sharedtest.DeletedItem(testMetric1.Version + 1)
		_, _ = store.Upsert(ldstoreimpl.Metrics(), testMetric1.Key, deletedItem)

		assert.Equal(t,
			sharedtest.ReceivedItemUpdate{
				Kind: ldstoreimpl.Metrics(),
				Key:  testMetric1.Key,
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

	t.Run("config override", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testIndexSamplingRatioOverridev2 := ldbuilders.NewConfigOverrideBuilder(testIndexSamplingRatioOverride.Key).Version(testIndexSamplingRatioOverride.Version + 1).Build()
		_, _ = sharedtest.UpsertConfigOverride(baseStore, testIndexSamplingRatioOverridev2)
		deletedItem := sharedtest.DeletedItem(testIndexSamplingRatioOverride.Version)
		_, _ = store.Upsert(ldstoreimpl.ConfigOverrides(), testIndexSamplingRatioOverride.Key, deletedItem)

		updates.expectItemUpdate(t)
	})

	t.Run("metric", func(t *testing.T) {
		baseStore, store, updates := makeTestComponents()
		testMetric1v2 := ldbuilders.NewMetricBuilder(testMetric1.Key).Version(testMetric1.Version + 1).Build()
		_, _ = sharedtest.UpsertMetric(baseStore, testMetric1v2)
		deletedItem := sharedtest.DeletedItem(testMetric1.Version)
		_, _ = store.Upsert(ldstoreimpl.Metrics(), testMetric1.Key, deletedItem)

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
