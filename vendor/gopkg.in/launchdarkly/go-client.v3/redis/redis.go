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

	ld "gopkg.in/launchdarkly/go-client.v3"
)

// A Redis-backed feature store.
type RedisFeatureStore struct {
	prefix    string
	pool      *r.Pool
	cache     *cache.Cache
	timeout   time.Duration
	logger    ld.Logger
	inited    bool
	initCheck sync.Once
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

// Constructs a new Redis-backed feature store connecting to the specified URL with a default
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

// Constructs a new Redis-backed feature store with the specified redigo pool configuration.
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

// Constructs a new Redis-backed feature store connecting to the specified host and port with a default
// connection pool configuration (16 concurrent connections, connection requests block).
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly"
func NewRedisFeatureStore(host string, port int, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	return NewRedisFeatureStoreFromUrl(fmt.Sprintf("redis://%s:%d", host, port), prefix, timeout, logger)
}

func (store *RedisFeatureStore) featuresKey(kind ld.VersionedDataKind) string {
	return store.prefix + ":" + kind.GetNamespace()
}

func (store *RedisFeatureStore) Get(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	item, err := store.getEvenIfDeleted(kind, key)
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

func (store *RedisFeatureStore) All(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {

	if store.cache != nil {
		if data, present := store.cache.Get(allFlagsCacheKey(kind)); present {
			if items, ok := data.(map[string]ld.VersionedData); ok {
				return items, nil
			} else {
				store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %T. Expected map[string]ld.VersionedData",
					data)
			}
		}
	}

	results := make(map[string]ld.VersionedData)

	c := store.getConn()
	defer c.Close()

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

func (store *RedisFeatureStore) Init(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	c := store.getConn()
	defer c.Close()

	c.Send("MULTI")

	if store.cache != nil {
		store.cache.Flush()
	}

	for kind, items := range allData {
		baseKey := store.featuresKey(kind)

		c.Send("DEL", baseKey)

		for k, v := range items {
			data, jsonErr := json.Marshal(v)

			if jsonErr != nil {
				return jsonErr
			}

			c.Send("HSET", baseKey, k, data)

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

func (store *RedisFeatureStore) Delete(kind ld.VersionedDataKind, key string, version int) error {
	c := store.getConn()
	defer c.Close()

	c.Send("WATCH", store.featuresKey(kind))
	defer c.Send("UNWATCH")

	item, err := store.getEvenIfDeleted(kind, key)

	if err != nil {
		return err
	}
	if item == nil || item.GetVersion() < version {
		deletedItem := kind.MakeDeletedItem(key, version)
		return store.put(c, kind, key, deletedItem)
	}
	return nil
}

func (store *RedisFeatureStore) Upsert(kind ld.VersionedDataKind, item ld.VersionedData) error {
	c := store.getConn()
	defer c.Close()

	c.Send("WATCH", store.featuresKey(kind))
	defer c.Send("UNWATCH")

	o, err := store.getEvenIfDeleted(kind, item.GetKey())

	if err != nil {
		return err
	}

	if o != nil && o.GetVersion() >= item.GetVersion() {
		return nil
	}
	return store.put(c, kind, item.GetKey(), item)
}

func cacheKey(kind ld.VersionedDataKind, key string) string {
	return kind.GetNamespace() + ":" + key
}

func allFlagsCacheKey(kind ld.VersionedDataKind) string {
	return "all:" + kind.GetNamespace()
}

func (store *RedisFeatureStore) getEvenIfDeleted(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	if store.cache != nil {
		if data, present := store.cache.Get(cacheKey(kind, key)); present {
			if item, ok := data.(ld.VersionedData); ok {
				return item, nil
			} else {
				store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %v. Expected ld.VersionedData",
					reflect.TypeOf(data))
			}
		}
	}

	c := store.getConn()
	defer c.Close()

	jsonStr, err := r.String(c.Do("HGET", store.featuresKey(kind), key))

	if err != nil {
		if err == r.ErrNil {
			store.logger.Printf("RedisFeatureStore: WARN: Key: %s not found in \"%s\"", key, kind.GetNamespace())
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
	return nil, fmt.Errorf("Unexpected data type from JSON unmarshal: %T", data)
}

func (store *RedisFeatureStore) put(c r.Conn, kind ld.VersionedDataKind, key string, item ld.VersionedData) error {
	data, jsonErr := json.Marshal(item)

	if jsonErr != nil {
		return jsonErr
	}

	_, err := c.Do("HSET", store.featuresKey(kind), key, data)

	if err == nil && store.cache != nil {
		store.cache.Delete(allFlagsCacheKey(kind))
		store.cache.Set(cacheKey(kind, key), item, store.timeout)
	}

	return err
}

func (store *RedisFeatureStore) Initialized() bool {
	store.initCheck.Do(func() {
		c := store.getConn()
		defer c.Close()
		inited, _ := r.Bool(c.Do("EXISTS", store.featuresKey(ld.Features)))
		store.inited = inited
	})
	return store.inited
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
}
