package cache

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// CacheHitCounter tracks the total number of cache hits
	CacheHitCounter = promauto.NewCounter(prometheus.CounterOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_hits_total",
		Help: "Total number of object cache hits",
	})

	// CacheMissCounter tracks the total number of cache misses
	CacheMissCounter = promauto.NewCounter(prometheus.CounterOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_misses_total",
		Help: "Total number of object cache misses",
	})

	// CacheUpdateCounter tracks the total number of cache updates with newer versions
	CacheUpdateCounter = promauto.NewCounter(prometheus.CounterOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_updates_total",
		Help: "Total number of cache updates with newer versions",
	})

	// CacheEvictionCounter tracks the total number of cache evictions
	CacheEvictionCounter = promauto.NewCounter(prometheus.CounterOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_evictions_total",
		Help: "Total number of cache evictions due to TTL or capacity limits",
	})

	// CacheObjectCountGauge tracks the current number of objects in cache
	CacheObjectCountGauge = promauto.NewGauge(prometheus.GaugeOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_objects_current",
		Help: "Current number of objects in cache",
	})

	// CacheHitRateGauge tracks the cache hit rate as a percentage
	CacheHitRateGauge = promauto.NewGauge(prometheus.GaugeOpts{ //nolint:gochecknoglobals
		Name: "ld_relay_object_cache_hit_rate",
		Help: "Cache hit rate as a percentage (0-100)",
	})
)

// UpdatePrometheusMetrics updates Prometheus metrics with current cache statistics

func (c *SharedObjectCache) UpdatePrometheusMetrics() {
	stats := c.GetStats()
	c.UpdatePrometheusMetricsWithStats(stats)
}

// UpdatePrometheusMetricsWithStats updates Prometheus metrics with provided statistics
// This method avoids potential deadlocks when called from within a locked context

func (c *SharedObjectCache) UpdatePrometheusMetricsWithStats(stats CacheStats) {
	// Update gauges
	CacheObjectCountGauge.Set(float64(stats.ObjectCount))
	CacheHitRateGauge.Set(stats.HitRate() * 100) // Convert to percentage
}

// IncrementHitMetrics increments hit-related metrics
func IncrementHitMetrics() {
	CacheHitCounter.Inc()
}

// IncrementMissMetrics increments miss-related metrics
func IncrementMissMetrics() {
	CacheMissCounter.Inc()
}

// IncrementUpdateMetrics increments update-related metrics
func IncrementUpdateMetrics() {
	CacheUpdateCounter.Inc()
}

// IncrementEvictionMetrics increments eviction-related metrics
func IncrementEvictionMetrics() {
	CacheEvictionCounter.Inc()
}
