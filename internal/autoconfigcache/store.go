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
	// SetAll writes the full PutContent to the store, removing stale items.
	SetAll(ctx context.Context, content autoconfig.PutContent) error
	// Upsert writes a single item to the store.
	Upsert(ctx context.Context, kind autoconfig.CacheKind, id string, data interface{}) error
	// Delete removes a single item from the store.
	Delete(ctx context.Context, kind autoconfig.CacheKind, id string) error
}

// NewStore creates a Store from the Relay config. Returns a Redis or DynamoDB-backed store when
// CacheKey is set and a backing store is configured; otherwise returns a noopStore.
func NewStore(c config.Config, loggers ldlog.Loggers) (Store, error) {
	cacheKey := strings.TrimSpace(c.AutoConfig.CacheKey)
	if !c.AutoConfig.Key.Defined() || cacheKey == "" {
		return noopStore{}, nil
	}
	if !c.Redis.URL.IsDefined() && !c.DynamoDB.Enabled {
		return noopStore{}, nil
	}
	encKey := resolveEncryptionKey(c)
	if c.Redis.URL.IsDefined() {
		return newRedisStore(c.Redis, cacheKey, encKey, loggers)
	}
	return newDynamoDBStore(c.DynamoDB, cacheKey, encKey, loggers)
}

const (
	envItemPrefix    = "env:"
	filterItemPrefix = "filter:"
)

// cacheField returns the storage key for a cached item based on its kind and ID.
func cacheField(kind autoconfig.CacheKind, id string) string {
	switch kind {
	case autoconfig.CacheKindEnvironment:
		return envItemPrefix + id
	case autoconfig.CacheKindFilter:
		return filterItemPrefix + id
	default:
		return id
	}
}

// noopStore is a cache that does nothing. Used when no persistent store is configured.
type noopStore struct{}

func (noopStore) GetAll(context.Context) (*autoconfig.PutContent, error)                  { return nil, nil }
func (noopStore) SetAll(context.Context, autoconfig.PutContent) error                     { return nil }
func (noopStore) Upsert(context.Context, autoconfig.CacheKind, string, interface{}) error { return nil }
func (noopStore) Delete(context.Context, autoconfig.CacheKind, string) error              { return nil }
func (noopStore) Close() error                                                            { return nil }
