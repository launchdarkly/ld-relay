package store

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/ld-relay/v8/internal/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreIntegrationWithCache(t *testing.T) {
	// Create a shared cache
	cacheConfig := cache.CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        time.Minute,
	}
	sharedCache := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

	// Create two store adapters that share the same cache
	updates1 := &mockEnvStreamUpdates{}
	updates2 := &mockEnvStreamUpdates{}

	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}

	adapter1 := NewSSERelayDataStoreAdapter(factory, updates1, sharedCache)
	adapter2 := NewSSERelayDataStoreAdapter(factory, updates2, sharedCache)

	// Build the stores (this creates the wrapper)
	testCtx := sharedtest.NewTestContext()
	store1, err := adapter1.Build(testCtx)
	require.NoError(t, err)

	store2, err := adapter2.Build(testCtx)
	require.NoError(t, err)

	// Create test data
	flag := &testFlag{Key: "shared-flag", Version: 1}
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	collection := ldstoretypes.Collection{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: "shared-flag", Item: descriptor},
		},
	}

	// Initialize both stores with the same data
	err = store1.Init([]ldstoretypes.Collection{collection})
	require.NoError(t, err)

	err = store2.Init([]ldstoretypes.Collection{collection})
	require.NoError(t, err)

	// Verify cache statistics
	stats := sharedCache.GetStats()
	assert.Equal(t, int64(1), stats.HitCount)    // Second init should be a cache hit
	assert.Equal(t, int64(1), stats.MissCount)   // First init should be a cache miss
	assert.Equal(t, int64(1), stats.ObjectCount) // One object in cache

	// Verify that both stores received the correct data
	assert.Len(t, updates1.allDataUpdates, 1)
	assert.Len(t, updates2.allDataUpdates, 1)
}

func TestStoreUpsertWithCache(t *testing.T) {
	// Create shared cache
	cacheConfig := cache.CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        time.Minute,
	}
	sharedCache := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}

	// Create store adapter
	updates := &mockEnvStreamUpdates{}
	adapter := NewSSERelayDataStoreAdapter(factory, updates, sharedCache)
	store, err := adapter.Build(sharedtest.NewTestContext())
	require.NoError(t, err)

	// Create test flag
	flag1 := &testFlag{Key: "test-flag", Version: 1}
	descriptor1 := ldstoretypes.ItemDescriptor{Version: 1, Item: flag1}

	// First upsert should be a cache miss
	updated, err := store.Upsert(ldstoreimpl.Features(), "test-flag", descriptor1)
	require.NoError(t, err)
	assert.True(t, updated)

	// Create newer version of the same flag
	flag2 := &testFlag{Key: "test-flag", Version: 2}
	descriptor2 := ldstoretypes.ItemDescriptor{Version: 2, Item: flag2}

	// Second upsert with newer version should update cache
	updated, err = store.Upsert(ldstoreimpl.Features(), "test-flag", descriptor2)
	require.NoError(t, err)
	assert.True(t, updated)

	// Third upsert with same version should be cache hit
	updated, err = store.Upsert(ldstoreimpl.Features(), "test-flag", descriptor2)
	require.NoError(t, err)
	assert.False(t, updated) // Store already has this version

	// Verify cache statistics
	stats := sharedCache.GetStats()
	assert.Equal(t, int64(1), stats.HitCount)    // One cache hit for same version
	assert.Equal(t, int64(1), stats.MissCount)   // One cache miss for initial flag
	assert.Equal(t, int64(1), stats.UpdateCount) // One cache update for newer version
	assert.Equal(t, int64(1), stats.ObjectCount) // One object in cache

	// Verify updates were sent
	assert.Len(t, updates.singleItemUpdates, 3)
}

func TestCacheDisabledIntegration(t *testing.T) {
	// Create disabled cache
	cacheConfig := cache.CacheConfig{Enabled: false}
	sharedCache := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}

	// Create store adapter
	updates := &mockEnvStreamUpdates{}
	adapter := NewSSERelayDataStoreAdapter(factory, updates, sharedCache)
	store, err := adapter.Build(sharedtest.NewTestContext())
	require.NoError(t, err)

	// Create test data
	flag := &testFlag{Key: "test-flag", Version: 1}
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// Upsert should work normally but not use cache
	updated, err := store.Upsert(ldstoreimpl.Features(), "test-flag", descriptor)
	require.NoError(t, err)
	assert.True(t, updated)

	// Second upsert with same data
	updated, err = store.Upsert(ldstoreimpl.Features(), "test-flag", descriptor)
	require.NoError(t, err)
	assert.False(t, updated) // Store logic still applies

	// Cache should show no activity
	stats := sharedCache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount)
	assert.Equal(t, int64(0), stats.MissCount)
	assert.Equal(t, int64(0), stats.ObjectCount)
}

func TestMultipleEnvironmentsSharedCache(t *testing.T) {
	// Simulate multiple environments sharing a cache
	cacheConfig := cache.CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        time.Minute,
	}
	sharedCache := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

	// Create multiple store adapters representing different environments
	var stores []subsystems.DataStore
	var updates []*mockEnvStreamUpdates

	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}

	testCtx := sharedtest.NewTestContext()
	for i := 0; i < 3; i++ {
		update := &mockEnvStreamUpdates{}
		updates = append(updates, update)

		adapter := NewSSERelayDataStoreAdapter(factory, update, sharedCache)
		store, err := adapter.Build(testCtx)
		require.NoError(t, err)

		stores = append(stores, store)
	}

	// Create shared test data (same flag across environments)
	flag := &testFlag{Key: "shared-flag", Version: 1}
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	collection := ldstoretypes.Collection{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: "shared-flag", Item: descriptor},
		},
	}

	// Initialize all stores with the same data
	for i, store := range stores {
		err := store.Init([]ldstoretypes.Collection{collection})
		require.NoError(t, err, "Failed to initialize store %d", i)
	}

	// Verify cache statistics - should show memory savings
	stats := sharedCache.GetStats()
	assert.Equal(t, int64(2), stats.HitCount)    // 2 cache hits (stores 2 and 3)
	assert.Equal(t, int64(1), stats.MissCount)   // 1 cache miss (store 1)
	assert.Equal(t, int64(1), stats.ObjectCount) // Only one object stored, shared across stores

	// All stores should have received updates
	for i, update := range updates {
		assert.Len(t, update.allDataUpdates, 1, "Store %d should have received data update", i)
	}
}

func TestSeparateCachesForDifferentEnvironments(t *testing.T) {
	// Create two separate caches to simulate different environments/SDK keys
	cacheConfig := cache.CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        time.Minute,
	}

	cache1 := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())
	cache2 := cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

	// Create stores for each environment with their own cache
	store := sharedtest.NewInMemoryStore()
	factory := &mockStoreFactory{instance: store}

	updates1 := &mockEnvStreamUpdates{}
	adapter1 := NewSSERelayDataStoreAdapter(factory, updates1, cache1)
	testCtx := sharedtest.NewTestContext()
	store1, err := adapter1.Build(testCtx)
	require.NoError(t, err)

	updates2 := &mockEnvStreamUpdates{}
	adapter2 := NewSSERelayDataStoreAdapter(factory, updates2, cache2)
	store2, err := adapter2.Build(testCtx)
	require.NoError(t, err)

	// Create same flag with same key and version in both environments
	flag1 := &testFlag{Key: "shared-key", Version: 1}
	descriptor1 := ldstoretypes.ItemDescriptor{Version: 1, Item: flag1}

	flag2 := &testFlag{Key: "shared-key", Version: 1}
	descriptor2 := ldstoretypes.ItemDescriptor{Version: 1, Item: flag2}

	// Initialize both stores
	collection1 := ldstoretypes.Collection{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: "shared-key", Item: descriptor1},
		},
	}

	collection2 := ldstoretypes.Collection{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: "shared-key", Item: descriptor2},
		},
	}

	err = store1.Init([]ldstoretypes.Collection{collection1})
	require.NoError(t, err)

	err = store2.Init([]ldstoretypes.Collection{collection2})
	require.NoError(t, err)

	// Each cache should have its own stats - they should NOT share
	stats1 := cache1.GetStats()
	stats2 := cache2.GetStats()

	// Each cache should have 1 miss (no sharing between different caches)
	assert.Equal(t, int64(1), stats1.MissCount)
	assert.Equal(t, int64(1), stats2.MissCount)
	assert.Equal(t, int64(0), stats1.HitCount)
	assert.Equal(t, int64(0), stats2.HitCount)

	// Each cache should have 1 object
	assert.Equal(t, int64(1), stats1.ObjectCount)
	assert.Equal(t, int64(1), stats2.ObjectCount)

	// The objects should be different instances (not shared)
	// Note: We can't easily verify this without accessing internals, but the stats prove separation
}

func TestCacheIsolationAcrossDifferentSDKKeys(t *testing.T) {
	// Simulate multiple SDK keys (different projects/environments)
	// Each should have its own cache instance

	cacheConfig := cache.CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        time.Minute,
	}

	// Create 3 "environments" with different SDK keys
	numEnvironments := 3
	caches := make([]*cache.SharedObjectCache, numEnvironments)
	stores := make([]subsystems.DataStore, numEnvironments)

	for i := 0; i < numEnvironments; i++ {
		caches[i] = cache.NewSharedObjectCache(cacheConfig, ldlog.NewDefaultLoggers())

		store := sharedtest.NewInMemoryStore()
		factory := &mockStoreFactory{instance: store}
		updates := &mockEnvStreamUpdates{}
		adapter := NewSSERelayDataStoreAdapter(factory, updates, caches[i])

		testCtx := sharedtest.NewTestContext()
		builtStore, err := adapter.Build(testCtx)
		require.NoError(t, err)
		stores[i] = builtStore
	}

	// Add the same flag to all environments
	for i := 0; i < numEnvironments; i++ {
		flag := &testFlag{Key: "common-flag", Version: 1}
		descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

		collection := ldstoretypes.Collection{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: "common-flag", Item: descriptor},
			},
		}

		err := stores[i].Init([]ldstoretypes.Collection{collection})
		require.NoError(t, err)
	}

	// Each cache should be independent
	for i := 0; i < numEnvironments; i++ {
		stats := caches[i].GetStats()
		assert.Equal(t, int64(1), stats.ObjectCount, "Environment %d should have 1 object", i)
		assert.Equal(t, int64(1), stats.MissCount, "Environment %d should have 1 miss (no cross-cache sharing)", i)
		assert.Equal(t, int64(0), stats.HitCount, "Environment %d should have 0 hits", i)
	}

	// Total objects across all caches should equal number of environments
	// (each cache has its own copy)
	totalObjects := int64(0)
	for i := 0; i < numEnvironments; i++ {
		totalObjects += caches[i].GetStats().ObjectCount
	}
	assert.Equal(t, int64(numEnvironments), totalObjects)
}

// Mock implementation of EnvStreamUpdates for testing
type mockEnvStreamUpdates struct {
	allDataUpdates    [][]ldstoretypes.Collection
	singleItemUpdates []singleItemUpdate
}

type singleItemUpdate struct {
	kind ldstoretypes.DataKind
	key  string
	item ldstoretypes.ItemDescriptor
}

func (m *mockEnvStreamUpdates) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	m.allDataUpdates = append(m.allDataUpdates, allData)
}

func (m *mockEnvStreamUpdates) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	m.singleItemUpdates = append(m.singleItemUpdates, singleItemUpdate{
		kind: kind,
		key:  key,
		item: item,
	})
}

func (m *mockEnvStreamUpdates) InvalidateClientSideState() {
	// No-op for testing
}

// Test types

type testFlag struct {
	Key     string
	Version int
}
