package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSharedObjectCache(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        5 * time.Minute,
	}
	loggers := ldlog.NewDefaultLoggers()

	cache := NewSharedObjectCache(config, loggers)

	assert.NotNil(t, cache)
	assert.Equal(t, config, cache.GetConfig())
	assert.True(t, cache.IsEnabled())
	assert.NotNil(t, cache.flags)
	assert.NotNil(t, cache.segments)
}

func TestDefaultCacheConfig(t *testing.T) {
	config := DefaultCacheConfig()

	assert.False(t, config.Enabled) // Should be disabled by default
	assert.Equal(t, 10000, config.MaxObjects)
	assert.Equal(t, 5*time.Minute, config.TTL)
}

func TestCacheDisabled(t *testing.T) {
	config := CacheConfig{Enabled: false}
	loggers := ldlog.NewDefaultLoggers()
	cache := NewSharedObjectCache(config, loggers)

	// Create a test feature flag
	flag := createTestFlag("test-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// DeduplicateItem should return the original item when cache is disabled
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
	assert.Equal(t, descriptor, result)

	// Stats should remain at zero
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount)
	assert.Equal(t, int64(0), stats.MissCount)
}

func TestCacheMiss(t *testing.T) {
	cache := createTestCache()
	flag := createTestFlag("test-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// First access should be a cache miss
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
	assert.Equal(t, descriptor, result)

	// Stats should show one miss
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount)
	assert.Equal(t, int64(1), stats.MissCount)
	assert.Equal(t, int64(1), stats.ObjectCount)
}

func TestCacheHit(t *testing.T) {
	cache := createTestCache()
	flag := createTestFlag("test-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// First access - cache miss
	cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)

	// Second access with same version should be a cache hit
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
	assert.Equal(t, descriptor.Version, result.Version)
	assert.Same(t, flag, result.Item) // Should return the exact same object

	// Stats should show one hit and one miss
	stats := cache.GetStats()
	assert.Equal(t, int64(1), stats.HitCount)
	assert.Equal(t, int64(1), stats.MissCount)
	assert.Equal(t, int64(1), stats.ObjectCount)
	assert.Equal(t, 0.5, stats.HitRate())
}

func TestCacheUpdate(t *testing.T) {
	cache := createTestCache()
	flag1 := createTestFlag("test-flag", 1)
	flag2 := createTestFlag("test-flag", 2)

	descriptor1 := ldstoretypes.ItemDescriptor{Version: 1, Item: flag1}
	descriptor2 := ldstoretypes.ItemDescriptor{Version: 2, Item: flag2}

	// First access with version 1
	cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor1)

	// Second access with version 2 should update the cache
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor2)
	assert.Equal(t, descriptor2, result)

	// Stats should show one update
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount)
	assert.Equal(t, int64(1), stats.MissCount)
	assert.Equal(t, int64(1), stats.UpdateCount)
	assert.Equal(t, int64(1), stats.ObjectCount)
}

func TestCacheOlderVersion(t *testing.T) {
	cache := createTestCache()
	flag1 := createTestFlag("test-flag", 2)
	flag2 := createTestFlag("test-flag", 1)

	descriptor1 := ldstoretypes.ItemDescriptor{Version: 2, Item: flag1}
	descriptor2 := ldstoretypes.ItemDescriptor{Version: 1, Item: flag2}

	// First access with version 2
	cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor1)

	// Second access with version 1 (older) should not update cache
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor2)
	assert.Equal(t, descriptor2, result) // Should return the input descriptor as-is

	// The cached object should still have version 2 (not updated)
	cachedObj := cache.flags["test-flag"]
	assert.Equal(t, 2, cachedObj.Version)
	assert.Same(t, flag1, cachedObj.Data)

	// Stats should show no hits, no updates - older versions are ignored
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount, "Older version should not be counted as a hit")
	assert.Equal(t, int64(1), stats.MissCount, "Initial cache should be a miss")
	assert.Equal(t, int64(0), stats.UpdateCount, "Older version should not trigger update")
	assert.Equal(t, int64(1), stats.ObjectCount)
}

func TestDeduplicateCollections(t *testing.T) {
	cache := createTestCache()
	flag1 := createTestFlag("flag1", 1)
	flag2 := createTestFlag("flag2", 1)

	collection := ldstoretypes.Collection{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: "flag1", Item: ldstoretypes.ItemDescriptor{Version: 1, Item: flag1}},
			{Key: "flag2", Item: ldstoretypes.ItemDescriptor{Version: 1, Item: flag2}},
		},
	}

	// First call should cache both flags
	result := cache.DeduplicateCollections([]ldstoretypes.Collection{collection})
	require.Len(t, result, 1)
	assert.Len(t, result[0].Items, 2)

	// Second call with same data should return cached objects
	result2 := cache.DeduplicateCollections([]ldstoretypes.Collection{collection})
	require.Len(t, result2, 1)
	assert.Same(t, flag1, result2[0].Items[0].Item.Item)
	assert.Same(t, flag2, result2[0].Items[1].Item.Item)

	// Stats should show 2 misses and 2 hits
	stats := cache.GetStats()
	assert.Equal(t, int64(2), stats.HitCount)
	assert.Equal(t, int64(2), stats.MissCount)
	assert.Equal(t, int64(2), stats.ObjectCount)
}

func TestGetObjectMapForDifferentKinds(t *testing.T) {
	cache := createTestCache()

	// Test with features kind
	flagsMap := cache.getObjectMap(ldstoreimpl.Features())
	assert.Equal(t, cache.flags, flagsMap)

	// Test with segments kind
	segmentsMap := cache.getObjectMap(ldstoreimpl.Segments())
	assert.Equal(t, cache.segments, segmentsMap)

	// Test with unknown kind - should return empty map that doesn't affect cache
	unknownKind := &testDataKind{name: "unknown"}
	unknownMap := cache.getObjectMap(unknownKind)
	assert.NotNil(t, unknownMap)
	assert.Len(t, unknownMap, 0)
	
	// Verify that modifying the returned map doesn't affect the cache
	unknownMap["test"] = &CachedObject{Key: "test"}
	assert.Len(t, cache.flags, 0)    // Cache flags should still be empty
	assert.Len(t, cache.segments, 0) // Cache segments should still be empty
}

func TestCleanupExpiredObjects(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        100 * time.Millisecond, // Very short TTL for testing
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	flag := createTestFlag("test-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// Add object to cache
	cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
	assert.Equal(t, int64(1), cache.GetStats().ObjectCount)

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Run cleanup
	cache.cleanup()

	// Object should be evicted
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.ObjectCount)
	assert.Equal(t, int64(1), stats.EvictionCount)
}

func TestMaxObjectsLimit(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 2,         // Very low limit for testing
		TTL:        time.Hour, // Long TTL so objects don't expire during test
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Add 3 objects to exceed the limit
	for i := 1; i <= 3; i++ {
		flag := createTestFlag(fmt.Sprintf("flag-%d", i), i)
		descriptor := ldstoretypes.ItemDescriptor{Version: i, Item: flag}
		cache.DeduplicateItem(ldstoreimpl.Features(), fmt.Sprintf("flag-%d", i), descriptor)

		// Add a small delay so objects have different creation times
		time.Sleep(time.Millisecond)
	}

	// Should have 3 objects before cleanup
	assert.Equal(t, int64(3), cache.GetStats().ObjectCount)

	// Run cleanup to enforce limit
	cache.cleanup()

	// Should have only 2 objects after cleanup (oldest evicted)
	stats := cache.GetStats()
	assert.Equal(t, int64(2), stats.ObjectCount)
	assert.Equal(t, int64(1), stats.EvictionCount)
}

func TestStartCleanupRoutine(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        50 * time.Millisecond,
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Add an object
	flag := createTestFlag("test-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
	cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)

	// Start cleanup routine
	cache.StartCleanupRoutine()

	// Try to start cleanup routine again - should be ignored
	cache.StartCleanupRoutine()

	// Wait for cleanup to run and evict the expired object
	time.Sleep(100 * time.Millisecond)

	// Object should be cleaned up since TTL has expired
	assert.Equal(t, int64(0), cache.GetStats().ObjectCount)

	// Stop cleanup routine
	cache.StopCleanupRoutine()

	// Wait a bit to ensure cleanup routine has stopped
	time.Sleep(10 * time.Millisecond)
}

func TestStopCleanupRoutine(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        1 * time.Minute, // Long TTL so cleanup doesn't run during test
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Start cleanup routine
	cache.StartCleanupRoutine()

	// Verify cleanup routine is running
	cache.mu.RLock()
	assert.True(t, cache.cleanupStarted)
	assert.NotNil(t, cache.cleanupCancel)
	cache.mu.RUnlock()

	// Stop cleanup routine
	cache.StopCleanupRoutine()

	// Verify cleanup routine has stopped
	cache.mu.RLock()
	assert.False(t, cache.cleanupStarted)
	assert.Nil(t, cache.cleanupCancel)
	cache.mu.RUnlock()
}

func TestCacheWithNilItem(t *testing.T) {
	cache := createTestCache()
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: nil}

	// Cache should return the descriptor as-is for nil items
	result := cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
	assert.Equal(t, descriptor, result)

	// No cache operations should occur
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.HitCount)
	assert.Equal(t, int64(0), stats.MissCount)
	assert.Equal(t, int64(0), stats.ObjectCount)
}

func TestConcurrentWrites(t *testing.T) {
	cache := createTestCache()
	numGoroutines := 100
	numItemsPerGoroutine := 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Each goroutine writes different items
	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < numItemsPerGoroutine; j++ {
				key := fmt.Sprintf("flag-%d-%d", goroutineID, j)
				flag := createTestFlag(key, 1)
				descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
				cache.DeduplicateItem(ldstoreimpl.Features(), key, descriptor)
			}
		}(i)
	}

	wg.Wait()

	// All items should be cached
	stats := cache.GetStats()
	expectedCount := int64(numGoroutines * numItemsPerGoroutine)
	assert.Equal(t, expectedCount, stats.ObjectCount)
	assert.Equal(t, expectedCount, stats.MissCount)
	assert.Equal(t, int64(0), stats.HitCount)
}

func TestConcurrentReads(t *testing.T) {
	cache := createTestCache()
	flag := createTestFlag("shared-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}

	// Prime the cache
	cache.DeduplicateItem(ldstoreimpl.Features(), "shared-flag", descriptor)

	numReaders := 100
	var wg sync.WaitGroup
	wg.Add(numReaders)

	// Multiple goroutines read the same item concurrently
	for i := 0; i < numReaders; i++ {
		go func() {
			defer wg.Done()
			result := cache.DeduplicateItem(ldstoreimpl.Features(), "shared-flag", descriptor)
			assert.Equal(t, descriptor.Version, result.Version)
			assert.Same(t, flag, result.Item)
		}()
	}

	wg.Wait()

	// All reads should be cache hits
	stats := cache.GetStats()
	assert.Equal(t, int64(numReaders), stats.HitCount)
	assert.Equal(t, int64(1), stats.MissCount) // Initial cache
	assert.Equal(t, int64(1), stats.ObjectCount)
}

func TestConcurrentUpdates(t *testing.T) {
	cache := createTestCache()
	numUpdates := 50

	var wg sync.WaitGroup
	wg.Add(numUpdates)

	// Multiple goroutines update the same key with incrementing versions
	for i := 1; i <= numUpdates; i++ {
		go func(version int) {
			defer wg.Done()
			flag := createTestFlag("test-flag", version)
			descriptor := ldstoretypes.ItemDescriptor{Version: version, Item: flag}
			cache.DeduplicateItem(ldstoreimpl.Features(), "test-flag", descriptor)
		}(i)
	}

	wg.Wait()

	// Cache should have the highest version
	cachedObj := cache.flags["test-flag"]
	require.NotNil(t, cachedObj)
	assert.Equal(t, numUpdates, cachedObj.Version)

	// Should have some combination of misses and updates
	stats := cache.GetStats()
	assert.Equal(t, int64(1), stats.ObjectCount)
	assert.True(t, stats.MissCount >= 1, "Should have at least one miss")
	assert.True(t, stats.UpdateCount >= 1, "Should have at least one update")

	// Due to concurrent execution, some goroutines may see older versions
	// which are ignored (not counted as miss, hit, or update)
	// So we can only assert that we have some combination of operations
	assert.True(t, stats.MissCount+stats.HitCount+stats.UpdateCount > 0,
		"Should have recorded some operations")
	assert.True(t, stats.MissCount+stats.HitCount+stats.UpdateCount <= int64(numUpdates),
		"Total operations should not exceed number of updates")
}

func TestConcurrentMixedOperations(t *testing.T) {
	cache := createTestCache()
	numOperations := 100

	var wg sync.WaitGroup
	wg.Add(numOperations * 3) // reads, writes, updates

	// Concurrent reads
	for i := 0; i < numOperations; i++ {
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("flag-%d", id%10) // Reuse some keys
			flag := createTestFlag(key, 1)
			descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
			cache.DeduplicateItem(ldstoreimpl.Features(), key, descriptor)
		}(i)
	}

	// Concurrent writes to new keys
	for i := 0; i < numOperations; i++ {
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("new-flag-%d", id)
			flag := createTestFlag(key, 1)
			descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
			cache.DeduplicateItem(ldstoreimpl.Features(), key, descriptor)
		}(i)
	}

	// Concurrent updates to existing keys
	for i := 0; i < numOperations; i++ {
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("flag-%d", id%10)
			flag := createTestFlag(key, 2) // Higher version
			descriptor := ldstoretypes.ItemDescriptor{Version: 2, Item: flag}
			cache.DeduplicateItem(ldstoreimpl.Features(), key, descriptor)
		}(i)
	}

	wg.Wait()

	// Verify no panics and cache is still consistent
	stats := cache.GetStats()
	assert.True(t, stats.ObjectCount > 0, "Should have cached some objects")
	assert.True(t, stats.MissCount > 0, "Should have some cache misses")
	// No strict assertions on exact counts due to race conditions in operation ordering
}

func TestPrometheusMetricsUpdate(t *testing.T) {
	cache := createTestCache()

	// Add some items to generate statistics
	for i := 0; i < 5; i++ {
		flag := createTestFlag(fmt.Sprintf("flag-%d", i), 1)
		descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
		cache.DeduplicateItem(ldstoreimpl.Features(), fmt.Sprintf("flag-%d", i), descriptor)
	}

	// Access an item to generate a cache hit
	flag := createTestFlag("flag-0", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
	cache.DeduplicateItem(ldstoreimpl.Features(), "flag-0", descriptor)

	// Get stats
	stats := cache.GetStats()
	assert.Equal(t, int64(5), stats.MissCount)
	assert.Equal(t, int64(1), stats.HitCount)
	assert.Equal(t, int64(5), stats.ObjectCount)

	// Update Prometheus metrics (should not panic)
	cache.UpdatePrometheusMetrics()
	cache.UpdatePrometheusMetricsWithStats(stats)

	// Verify increment functions can be called (should not panic)
	IncrementHitMetrics()
	IncrementMissMetrics()
	IncrementUpdateMetrics()
	IncrementEvictionMetrics()
}

func TestCacheStatsHitRate(t *testing.T) {
	tests := []struct {
		name     string
		hits     int64
		misses   int64
		expected float64
	}{
		{"No operations", 0, 0, 0.0},
		{"Only hits", 10, 0, 1.0},
		{"Only misses", 0, 10, 0.0},
		{"50% hit rate", 5, 5, 0.5},
		{"80% hit rate", 80, 20, 0.8},
		{"20% hit rate", 20, 80, 0.2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := CacheStats{
				HitCount:  tt.hits,
				MissCount: tt.misses,
			}
			assert.InDelta(t, tt.expected, stats.HitRate(), 0.001)
		})
	}
}

func TestCleanupRoutineLifecycle(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        100 * time.Millisecond,
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Start cleanup routine
	cache.StartCleanupRoutine()

	// Verify it's running
	cache.mu.RLock()
	assert.True(t, cache.cleanupStarted)
	assert.NotNil(t, cache.cleanupCancel)
	cache.mu.RUnlock()

	// Add objects that will expire
	for i := 0; i < 10; i++ {
		flag := createTestFlag(fmt.Sprintf("flag-%d", i), 1)
		descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
		cache.DeduplicateItem(ldstoreimpl.Features(), fmt.Sprintf("flag-%d", i), descriptor)
	}

	initialCount := cache.GetStats().ObjectCount
	assert.Equal(t, int64(10), initialCount)

	// Wait for objects to expire and cleanup to run
	time.Sleep(250 * time.Millisecond)

	// Objects should be evicted
	stats := cache.GetStats()
	assert.Equal(t, int64(0), stats.ObjectCount, "All objects should be evicted after TTL")
	assert.True(t, stats.EvictionCount > 0, "Should have evictions")

	// Stop cleanup routine
	cache.StopCleanupRoutine()

	// Verify it's stopped
	cache.mu.RLock()
	assert.False(t, cache.cleanupStarted)
	assert.Nil(t, cache.cleanupCancel)
	cache.mu.RUnlock()

	// Add more objects after stopping
	flag := createTestFlag("new-flag", 1)
	descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
	cache.DeduplicateItem(ldstoreimpl.Features(), "new-flag", descriptor)

	// Wait to ensure cleanup doesn't run
	time.Sleep(250 * time.Millisecond)

	// Object should still be there (cleanup not running)
	assert.Equal(t, int64(1), cache.GetStats().ObjectCount)
}

func TestCleanupRoutineMultipleStartStopCycles(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        200 * time.Millisecond,
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Multiple start/stop cycles should work correctly
	for cycle := 0; cycle < 3; cycle++ {
		// Start
		cache.StartCleanupRoutine()

		cache.mu.RLock()
		assert.True(t, cache.cleanupStarted, "Cycle %d: should be started", cycle)
		cache.mu.RUnlock()

		// Add objects
		flag := createTestFlag(fmt.Sprintf("flag-%d", cycle), 1)
		descriptor := ldstoretypes.ItemDescriptor{Version: 1, Item: flag}
		cache.DeduplicateItem(ldstoreimpl.Features(), fmt.Sprintf("flag-%d", cycle), descriptor)

		// Stop
		cache.StopCleanupRoutine()

		cache.mu.RLock()
		assert.False(t, cache.cleanupStarted, "Cycle %d: should be stopped", cycle)
		cache.mu.RUnlock()
	}
}

func TestCleanupRoutineWithContextCancellation(t *testing.T) {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        5 * time.Minute, // Long TTL so cleanup doesn't run naturally
	}
	cache := NewSharedObjectCache(config, ldlog.NewDefaultLoggers())

	// Start cleanup routine
	cache.StartCleanupRoutine()

	// Verify it's running
	cache.mu.RLock()
	started := cache.cleanupStarted
	cache.mu.RUnlock()
	assert.True(t, started)

	// Stop immediately (simulates rapid shutdown)
	cache.StopCleanupRoutine()

	// Should be fully stopped
	cache.mu.RLock()
	assert.False(t, cache.cleanupStarted)
	assert.Nil(t, cache.cleanupCancel)
	cache.mu.RUnlock()

	// Should be able to start again immediately
	cache.StartCleanupRoutine()

	cache.mu.RLock()
	assert.True(t, cache.cleanupStarted)
	cache.mu.RUnlock()

	cache.StopCleanupRoutine()
}

// Helper functions

func createTestCache() *SharedObjectCache {
	config := CacheConfig{
		Enabled:    true,
		MaxObjects: 100,
		TTL:        5 * time.Minute,
	}
	return NewSharedObjectCache(config, ldlog.NewDefaultLoggers())
}

func createTestFlag(key string, version int) *testFlag {
	return &testFlag{
		Key:     key,
		Version: version,
	}
}

// Test types

type testFlag struct {
	Key     string
	Version int
}

type testDataKind struct {
	name string
}

func (k *testDataKind) GetName() string {
	return k.name
}

func (k *testDataKind) Serialize(item ldstoretypes.ItemDescriptor) []byte {
	return nil
}

func (k *testDataKind) Deserialize(data []byte) (ldstoretypes.ItemDescriptor, error) {
	return ldstoretypes.ItemDescriptor{}, nil
}

