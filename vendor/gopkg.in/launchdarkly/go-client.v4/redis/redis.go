package redis

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	r "github.com/garyburd/redigo/redis"
	"github.com/patrickmn/go-cache"

	ld "gopkg.in/launchdarkly/go-client.v4"
)

// RedisFeatureStore is a Redis-backed feature store implementation.
type RedisFeatureStore struct { // nolint:golint // package name in type name
	prefix     string
	pool       *r.Pool
	cache      *cache.Cache
	timeout    time.Duration
	logger     ld.Logger
	testTxHook func()
	inited     bool
	initCheck  sync.Once
}

var pool *r.Pool

func newPool(url string) *r.Pool {
	pool = &r.Pool{
		MaxIdle:     20,
		MaxActive:   16,
		Wait:        true,
		IdleTimeout: 300 * time.Second,
		Dial: func() (c r.Conn, err error) {
			c, err = r.DialURL(url)
			return
		},
		TestOnBorrow: func(c r.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
	return pool
}

func (store *RedisFeatureStore) getConn() r.Conn {
	return store.pool.Get()
}

// NewRedisFeatureStoreFromUrl constructs a new Redis-backed feature store connecting to the specified URL with a default
// connection pool configuration (16 concurrent connections, connection requests block).
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly".
func NewRedisFeatureStoreFromUrl(url, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	if logger == nil {
		logger = defaultLogger()
	}
	logger.Printf("RedisFeatureStore: Using url: %s", url)
	return NewRedisFeatureStoreWithPool(newPool(url), prefix, timeout, logger)

}

// NewRedisFeatureStoreWithPool constructs a new Redis-backed feature store with the specified redigo pool configuration.
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly".
func NewRedisFeatureStoreWithPool(pool *r.Pool, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	var c *cache.Cache

	if logger == nil {
		logger = defaultLogger()
	}

	if prefix == "" {
		prefix = "launchdarkly"
	}
	logger.Printf("RedisFeatureStore: Using prefix: %s ", prefix)

	if timeout > 0 {
		logger.Printf("RedisFeatureStore: Using local cache with timeout: %v", timeout)
		c = cache.New(timeout, 5*time.Minute)
	}

	store := RedisFeatureStore{
		prefix:  prefix,
		pool:    pool,
		cache:   c,
		timeout: timeout,
		logger:  logger,
		inited:  false,
	}
	return &store
}

// NewRedisFeatureStore constructs a new Redis-backed feature store connecting to the specified host and port with a default
// connection pool configuration (16 concurrent connections, connection requests block).
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly"
func NewRedisFeatureStore(host string, port int, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	return NewRedisFeatureStoreFromUrl(fmt.Sprintf("redis://%s:%d", host, port), prefix, timeout, logger)
}

func (store *RedisFeatureStore) featuresKey(kind ld.VersionedDataKind) string {
	return store.prefix + ":" + kind.GetNamespace()
}

// Get returns an individual object of a given type from the store
func (store *RedisFeatureStore) Get(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	item, err := store.getEvenIfDeleted(kind, key, true)
	if err == nil && item == nil {
		store.logger.Printf("RedisFeatureStore: WARN: Item not found in store. Key: %s", key)
	}
	if err == nil && item != nil && item.IsDeleted() {
		store.logger.Printf("RedisFeatureStore: WARN: Attempted to get deleted item in \"%s\". Key: %s",
			kind.GetNamespace(), key)
		return nil, nil
	}
	return item, err
}

// All returns all the objects of a given kind from the store
func (store *RedisFeatureStore) All(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {

	if store.cache != nil {
		if data, present := store.cache.Get(allFlagsCacheKey(kind)); present {
			items, ok := data.(map[string]ld.VersionedData)
			if ok {
				return items, nil
			}
			store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %T. Expected map[string]ld.VersionedData", data)
		}
	}

	results := make(map[string]ld.VersionedData)

	c := store.getConn()
	defer c.Close() // nolint:errcheck

	values, err := r.StringMap(c.Do("HGETALL", store.featuresKey(kind)))

	if err != nil && err != r.ErrNil {
		return nil, err
	}

	for k, v := range values {
		item, jsonErr := store.unmarshalItem(kind, v)

		if jsonErr != nil {
			return nil, err
		}

		if !item.IsDeleted() {
			results[k] = item
		}
	}
	if store.cache != nil {
		store.cache.Set(allFlagsCacheKey(kind), results, store.timeout)
	}
	return results, nil
}

// Init populates the store with a complete set of versioned data
func (store *RedisFeatureStore) Init(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	c := store.getConn()
	defer c.Close() // nolint:errcheck

	_ = c.Send("MULTI")

	if store.cache != nil {
		store.cache.Flush()
	}

	for kind, items := range allData {
		baseKey := store.featuresKey(kind)

		_ = c.Send("DEL", baseKey)

		for k, v := range items {
			data, jsonErr := json.Marshal(v)

			if jsonErr != nil {
				return jsonErr
			}

			_ = c.Send("HSET", baseKey, k, data)

			if store.cache != nil {
				store.cache.Set(cacheKey(kind, k), v, store.timeout)
			}
		}

		if store.cache != nil {
			store.cache.Set(allFlagsCacheKey(kind), items, store.timeout)
		}
	}

	_, err := c.Do("EXEC")

	store.initCheck.Do(func() { store.inited = true })

	return err
}

// Delete removes an item of a given kind from the store
func (store *RedisFeatureStore) Delete(kind ld.VersionedDataKind, key string, version int) error {
	deletedItem := kind.MakeDeletedItem(key, version)
	return store.updateWithVersioning(kind, deletedItem)
}

// Upsert inserts or replaces an item in the store unless there it already contains an item with an equal or larger version
func (store *RedisFeatureStore) Upsert(kind ld.VersionedDataKind, item ld.VersionedData) error {
	return store.updateWithVersioning(kind, item)
}

func cacheKey(kind ld.VersionedDataKind, key string) string {
	return kind.GetNamespace() + ":" + key
}

func allFlagsCacheKey(kind ld.VersionedDataKind) string {
	return "all:" + kind.GetNamespace()
}

func (store *RedisFeatureStore) getEvenIfDeleted(kind ld.VersionedDataKind, key string, useCache bool) (ld.VersionedData, error) {
	if useCache && store.cache != nil {
		if data, present := store.cache.Get(cacheKey(kind, key)); present {
			item, ok := data.(ld.VersionedData)
			if ok {
				return item, nil
			}
			store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %v. Expected ld.VersionedData", reflect.TypeOf(data))
		}
	}

	c := store.getConn()
	defer c.Close() // nolint:errcheck

	jsonStr, err := r.String(c.Do("HGET", store.featuresKey(kind), key))

	if err != nil {
		if err == r.ErrNil {
			store.logger.Printf("RedisFeatureStore: DEBUG: Key: %s not found in \"%s\"", key, kind.GetNamespace())
			return nil, nil
		}
		return nil, err
	}

	item, jsonErr := store.unmarshalItem(kind, jsonStr)
	if jsonErr != nil {
		return nil, jsonErr
	}
	if store.cache != nil {
		store.cache.Set(cacheKey(kind, key), item, store.timeout)
	}
	return item, nil
}

func (store *RedisFeatureStore) unmarshalItem(kind ld.VersionedDataKind, jsonStr string) (ld.VersionedData, error) {
	data := kind.GetDefaultItem()
	if jsonErr := json.Unmarshal([]byte(jsonStr), &data); jsonErr != nil {
		return nil, jsonErr
	}
	if item, ok := data.(ld.VersionedData); ok {
		return item, nil
	}
	return nil, fmt.Errorf("unexpected data type from JSON unmarshal: %T", data)
}

func (store *RedisFeatureStore) updateWithVersioning(kind ld.VersionedDataKind, newItem ld.VersionedData) error {
	baseKey := store.featuresKey(kind)
	key := newItem.GetKey()
	for {
		// We accept that we can acquire multiple connections here and defer inside loop but we don't expect many
		c := store.getConn()
		defer c.Close() // nolint:errcheck

		_, err := c.Do("WATCH", baseKey)
		if err != nil {
			return err
		}

		defer c.Send("UNWATCH") // nolint:errcheck // this should always succeed

		if store.testTxHook != nil { // instrumentation for unit tests
			store.testTxHook()
		}

		oldItem, err := store.getEvenIfDeleted(kind, key, false)

		if err != nil {
			return err
		}

		if oldItem != nil && oldItem.GetVersion() >= newItem.GetVersion() {
			return nil
		}

		data, jsonErr := json.Marshal(newItem)
		if jsonErr != nil {
			return jsonErr
		}

		_ = c.Send("MULTI")
		err = c.Send("HSET", baseKey, key, data)
		if err == nil {
			var result interface{}
			result, err = c.Do("EXEC")
			if err == nil {
				if result == nil {
					// if exec returned nothing, it means the watch was triggered and we should retry
					store.logger.Printf("RedisFeatureStore: DEBUG: Concurrent modification detected, retrying")
					continue
				} else if store.cache != nil {
					store.cache.Delete(allFlagsCacheKey(kind))
					store.cache.Set(cacheKey(kind, key), newItem, store.timeout)
				}
			}
		}
		return err
	}
}

// Initialized returns whether redis contains an entry for this environment
func (store *RedisFeatureStore) Initialized() bool {
	store.initCheck.Do(func() {
		c := store.getConn()
		defer c.Close() // nolint:errcheck
		store.inited, _ = r.Bool(c.Do("EXISTS", store.featuresKey(ld.Features)))
	})
	return store.inited
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
}
