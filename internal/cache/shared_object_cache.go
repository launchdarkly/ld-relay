package cache

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// SharedObjectCache provides deduplication of feature flags and segments across multiple SDK instances.
// It uses a key-version strategy to ensure consistent caching across filtered environments.
type SharedObjectCache struct {
	mu             sync.RWMutex
	flags          map[string]*CachedObject // key: flag key
	segments       map[string]*CachedObject // key: segment key
	stats          CacheStats
	config         CacheConfig
	loggers        ldlog.Loggers
	cleanupStarted bool               // tracks if cleanup routine is already running
	cleanupCancel  context.CancelFunc // function to cancel the cleanup routine
	cleanupWg      sync.WaitGroup     // tracks when cleanup routine has fully stopped
}

// CachedObject represents a cached feature flag or segment with metadata for deduplication
type CachedObject struct {
	Data         interface{} // The actual object (flag, segment, etc.)
	Key          string      // Object key (flag/segment key)
	Version      int         // Object version
	SharedCount  int32       // Number of environments sharing this object (metric for deduplication effectiveness)
	LastAccessed time.Time   // Last access timestamp for LRU eviction and TTL
}

// CacheConfig defines configuration options for the shared object cache
type CacheConfig struct {
	Enabled    bool          // Whether the cache is enabled
	MaxObjects int           // Maximum number of cached objects
	TTL        time.Duration // Time-to-live for unused objects
}

// CacheStats tracks cache performance metrics
type CacheStats struct {
	HitCount      int64 // Number of cache hits (objects reused)
	MissCount     int64 // Number of cache misses (new objects cached)
	UpdateCount   int64 // Number of version updates to existing objects
	EvictionCount int64 // Number of objects evicted due to TTL or limits
	ObjectCount   int64 // Current number of objects in cache
}

// HitRate returns the cache hit rate as a percentage (0.0 to 1.0)
func (s *CacheStats) HitRate() float64 {
	total := s.HitCount + s.MissCount
	if total == 0 {
		return 0
	}
	return float64(s.HitCount) / float64(total)
}

// NewSharedObjectCache creates a new shared object cache with the given configuration
func NewSharedObjectCache(config CacheConfig, loggers ldlog.Loggers) *SharedObjectCache {
	return &SharedObjectCache{
		flags:    make(map[string]*CachedObject),
		segments: make(map[string]*CachedObject),
		config:   config,
		loggers:  loggers,
	}
}

// DefaultCacheConfig returns a default cache configuration
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		Enabled:    false, // Disabled by default for safety
		MaxObjects: 10_000,
		TTL:        5 * time.Minute,
	}
}

// DeduplicateCollections processes a slice of collections and deduplicates their items
func (c *SharedObjectCache) DeduplicateCollections(collections []ldstoretypes.Collection) []ldstoretypes.Collection {
	if !c.config.Enabled {
		return collections
	}

	result := make([]ldstoretypes.Collection, len(collections))

	for i, coll := range collections {
		deduplicatedItems := make([]ldstoretypes.KeyedItemDescriptor, len(coll.Items))

		for j, keyedItem := range coll.Items {
			deduplicatedItems[j] = ldstoretypes.KeyedItemDescriptor{
				Key:  keyedItem.Key,
				Item: c.DeduplicateItem(coll.Kind, keyedItem.Key, keyedItem.Item),
			}
		}

		result[i] = ldstoretypes.Collection{
			Kind:  coll.Kind,
			Items: deduplicatedItems,
		}
	}

	return result
}

// DeduplicateItem processes a single item descriptor and returns either the cached version
// or updates the cache with the new version
func (c *SharedObjectCache) DeduplicateItem(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) ldstoretypes.ItemDescriptor {
	if !c.config.Enabled || item.Item == nil {
		return item
	}

	objectMap := c.getObjectMap(kind)

	c.mu.Lock()
	defer c.mu.Unlock()

	if cached, exists := objectMap[key]; exists {
		// Check if this is a newer version
		switch {
		case item.Version > cached.Version:
			// Update cache with newer version
			cached.Data = item.Item
			cached.Version = item.Version
			cached.LastAccessed = time.Now()
			atomic.AddInt64(&c.stats.UpdateCount, 1)
			IncrementUpdateMetrics()
		case item.Version == cached.Version:
			// Same version - another environment is now sharing this object
			atomic.AddInt32(&cached.SharedCount, 1)
			cached.LastAccessed = time.Now()
			atomic.AddInt64(&c.stats.HitCount, 1)
			IncrementHitMetrics()

			return ldstoretypes.ItemDescriptor{
				Version: cached.Version,
				Item:    cached.Data,
			}
		default:
			// Older version - still cache it but log warning
			c.loggers.Warnf("Received older version %d for %s %s (cached: %d)",
				item.Version, kind.GetName(), key, cached.Version)
		}
	} else {
		// Cache miss - store new object (first environment using it)
		cached := &CachedObject{
			Data:         item.Item,
			Key:          key,
			Version:      item.Version,
			SharedCount:  1,
			LastAccessed: time.Now(),
		}

		objectMap[key] = cached
		atomic.AddInt64(&c.stats.MissCount, 1)
		atomic.AddInt64(&c.stats.ObjectCount, 1)
		IncrementMissMetrics()
	}

	return item
}

// getObjectMap returns the appropriate cache map for the given data kind
func (c *SharedObjectCache) getObjectMap(kind ldstoretypes.DataKind) map[string]*CachedObject {
	switch kind.GetName() {
	case "features":
		return c.flags
	case "segments":
		return c.segments
	default:
		// For unknown kinds, create a temporary map (no caching)
		c.loggers.Warnf("Unknown data kind for caching: %s", kind.GetName())
		return make(map[string]*CachedObject)
	}
}

// StartCleanupRoutine starts a background goroutine that periodically cleans up expired objects.
// The routine can be stopped by calling StopCleanupRoutine.
func (c *SharedObjectCache) StartCleanupRoutine() {
	if !c.config.Enabled {
		return
	}

	c.mu.Lock()
	if c.cleanupStarted {
		c.mu.Unlock()
		c.loggers.Debug("Cache cleanup routine already running, skipping duplicate start")
		return
	}
	c.cleanupStarted = true

	// Create a cancellable context for this cleanup routine
	cleanupCtx, cancel := context.WithCancel(context.Background())
	c.cleanupCancel = cancel
	c.mu.Unlock()

	c.loggers.Info("Started object cache cleanup routine")

	c.cleanupWg.Add(1)
	go func() {
		defer c.cleanupWg.Done()
		ticker := time.NewTicker(c.config.TTL / 2)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.cleanup()
			case <-cleanupCtx.Done():
				c.loggers.Info("Stopping object cache cleanup routine")
				return
			}
		}
	}()
}

// StopCleanupRoutine stops the cleanup routine if it's running and waits for it to fully exit
func (c *SharedObjectCache) StopCleanupRoutine() {
	c.mu.Lock()

	if !c.config.Enabled {
		c.mu.Unlock()
		return
	}

	if c.cleanupCancel == nil {
		c.mu.Unlock()
		return
	}

	c.loggers.Info("Stopping object cache cleanup routine if running")

	// Signal cancellation
	c.cleanupCancel()
	c.cleanupCancel = nil
	c.cleanupStarted = false

	// Release lock before waiting to avoid deadlock
	c.mu.Unlock()

	// Wait for goroutine to fully exit
	c.cleanupWg.Wait()
}

// cleanup removes expired objects and enforces cache size limits
func (c *SharedObjectCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// Clean up flags and segments
	c.cleanupObjectMap(c.flags, now)
	c.cleanupObjectMap(c.segments, now)

	// Enforce max objects limit with LRU eviction
	if c.stats.ObjectCount > int64(c.config.MaxObjects) {
		c.evictLRU()
	}

	c.loggers.Debugf("Cache cleanup completed - objects: %d, hit rate: %.2f%%",
		c.stats.ObjectCount, c.stats.HitRate()*100)

	// Update Prometheus metrics with current stats (avoid deadlock by passing stats directly)
	c.UpdatePrometheusMetricsWithStats(c.stats)
}

// cleanupObjectMap removes expired objects from the given map
func (c *SharedObjectCache) cleanupObjectMap(objectMap map[string]*CachedObject, now time.Time) {
	for key, obj := range objectMap {
		// Remove objects that have exceeded their TTL
		if now.Sub(obj.LastAccessed) > c.config.TTL {
			delete(objectMap, key)
			atomic.AddInt64(&c.stats.EvictionCount, 1)
			atomic.AddInt64(&c.stats.ObjectCount, -1)
			IncrementEvictionMetrics()
		}
	}
}

// evictLRU evicts the least recently used objects when cache is over capacity
func (c *SharedObjectCache) evictLRU() {
	// Collect all objects with their last access times
	type objWithTime struct {
		key    string
		obj    *CachedObject
		objMap map[string]*CachedObject
	}

	allObjects := make([]objWithTime, 0, len(c.flags)+len(c.segments))

	for key, obj := range c.flags {
		allObjects = append(allObjects, objWithTime{key: key, obj: obj, objMap: c.flags})
	}
	for key, obj := range c.segments {
		allObjects = append(allObjects, objWithTime{key: key, obj: obj, objMap: c.segments})
	}

	// Sort by last access time (least recently used first)
	sort.Slice(allObjects, func(i, j int) bool {
		return allObjects[i].obj.LastAccessed.Before(allObjects[j].obj.LastAccessed)
	})

	// Evict oldest objects until we're under the limit
	objectsToEvict := c.stats.ObjectCount - int64(c.config.MaxObjects)
	for i := 0; i < int(objectsToEvict) && i < len(allObjects); i++ {
		obj := allObjects[i]
		delete(obj.objMap, obj.key)
		atomic.AddInt64(&c.stats.EvictionCount, 1)
		atomic.AddInt64(&c.stats.ObjectCount, -1)
		IncrementEvictionMetrics()
	}
}

// GetStats returns a copy of the current cache statistics
func (c *SharedObjectCache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// GetConfig returns the cache configuration
func (c *SharedObjectCache) GetConfig() CacheConfig {
	return c.config
}

// IsEnabled returns whether the cache is enabled
func (c *SharedObjectCache) IsEnabled() bool {
	return c.config.Enabled
}
