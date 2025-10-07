package cache

import (
	"github.com/launchdarkly/ld-relay/v8/config"
)

// NewCacheConfigFromRelay converts relay configuration to cache configuration
func NewCacheConfigFromRelay(objectCacheConfig config.ObjectCacheConfig) CacheConfig {
	cfg := DefaultCacheConfig()

	cfg.Enabled = objectCacheConfig.Enabled
	cfg.MaxObjects = objectCacheConfig.MaxObjects.GetOrElse(cfg.MaxObjects) // Use default max objects if not set
	cfg.TTL = objectCacheConfig.TTL.GetOrElse(cfg.TTL)                      // Use default TTL if not set

	return cfg
}
