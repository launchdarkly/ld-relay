package autoconfigcache

import (
	"context"
	"io"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
)

// Store reads and writes the encrypted AutoConfig cache in the configured persistent store (Redis or DynamoDB).
// Each environment and filter is stored as an individual item, encrypted separately.
// The cache key namespaces entries so multiple Relay instances can share the same store.
type Store interface {
	io.Closer
	// GetAll returns the cached PutContent, or nil if the cache is empty.
	GetAll(ctx context.Context) (*autoconfig.PutContent, error)
	// SetAll writes the PutContent to the store, storing each environment and filter as an individual item.
	// Stale items that are no longer in the content are removed.
	SetAll(ctx context.Context, content autoconfig.PutContent) error
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
