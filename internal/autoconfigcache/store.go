package autoconfigcache

import (
	"context"
	"io"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

// Store reads and writes the encrypted AutoConfig cache in the configured persistent store (Redis or DynamoDB).
// The cache key is used so multiple Relay instances can share the same store with different namespaced entries.
type Store interface {
	io.Closer
	// Get returns the cached value for the configured cache key, or nil if missing/error.
	Get(ctx context.Context) ([]byte, error)
	// Set writes the encrypted value for the configured cache key.
	Set(ctx context.Context, value []byte) error
}

// NewStore creates a Store from the Relay config when InitFromStoreFirst is enabled with Redis or DynamoDB.
// Returns nil when InitFromStoreFirst is false or no store is configured.
func NewStore(c config.Config, loggers ldlog.Loggers) (Store, error) {
	if !c.AutoConfig.Key.Defined() || !c.AutoConfig.InitFromStoreFirst {
		return nil, nil
	}
	cacheKey := strings.TrimSpace(c.AutoConfig.CacheKey)
	if cacheKey == "" {
		return nil, nil
	}
	if !c.Redis.URL.IsDefined() && !c.DynamoDB.Enabled {
		return nil, nil
	}
	encKey, err := resolveEncryptionKey(c)
	if err != nil {
		return nil, err
	}
	if c.Redis.URL.IsDefined() {
		return newRedisStore(c.Redis, cacheKey, encKey, loggers)
	}
	return newDynamoDBStore(c.DynamoDB, cacheKey, encKey, loggers)
}
