// Package redis provides a Redis-backed persistent feature store for the LaunchDarkly Go SDK.
//
// For more details about how and why you can use a persistent feature store, see:
// https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store
//
// To use the Redis feature store with the LaunchDarkly client:
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults()
//     if err != nil { ... }
//
//     config := ld.DefaultConfig
//     config.FeatureStore = store
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// The default Redis pool configuration uses an address of localhost:6379, a maximum of 16
// concurrent connections, and blocking connection requests. To customize any properties of
// the connection pool, use the Pool option or the NewRedisFeatureStoreWithPool constructor.
// You may also customize other properties of the feature store by providing options to
// NewRedisFeatureStoreWithDefaults, for example:
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.URL(myRedisURL),
//         redis.CacheTTL(30*time.Second))
//
// If you are also using Redis for other purposes, the feature store can coexist with
// other data as long as you are not using the same keys. By default, the keys used by the
// feature store will always start with "launchdarkly:"; you can change this to another
// prefix if desired.
package redis

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	r "github.com/garyburd/redigo/redis"

	ld "gopkg.in/launchdarkly/go-client.v4"
	"gopkg.in/launchdarkly/go-client.v4/utils"
)

const (
	// DefaultURL is the default URL for connecting to Redis, if you use
	// NewRedisFeatureStoreWithDefaults. You can specify otherwise with the RedisURL option.
	// If you are using the other constructors, you must specify the URL explicitly.
	DefaultURL = "redis://localhost:6379"
	// DefaultPrefix is a string that is prepended (along with a colon) to all Redis keys used
	// by the feature store. You can change this value with the Prefix() option for
	// NewRedisFeatureStoreWithDefaults, or with the "prefix" parameter to the other constructors.
	DefaultPrefix = "launchdarkly"
	// DefaultCacheTTL is the default amount of time that recently read or updated items will
	// be cached in memory, if you use NewRedisFeatureStoreWithDefaults. You can specify otherwise
	// with the CacheTTL option. If you are using the other constructors, their "timeout"
	// parameter serves the same purpose and there is no default.
	DefaultCacheTTL = 15 * time.Second
)

// FeatureStoreOption is the interface for optional configuration parameters that can be
// passed to NewRedisFeatureStoreWithDefaults. These include UseConfig, Prefix, CacheTTL, and UseLogger.
type FeatureStoreOption interface {
	apply(store *redisFeatureStoreCore) error
}

type redisURLOption struct {
	url string
}

func (o redisURLOption) apply(store *redisFeatureStoreCore) error {
	store.redisURL = o.url
	return nil
}

// URL creates an option for NewRedisFeatureStoreWithDefaults to specify the Redis host URL.
// If not specified, the default value is DefaultURL.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.URL("redis://my-redis-host:6379"))
func URL(url string) FeatureStoreOption {
	return redisURLOption{url}
}

// HostAndPort creates an option for NewRedisFeatureStoreWithDefaults to specify the Redis host
// address as a hostname and port.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.HostAndPort("my-redis-host", 6379))
func HostAndPort(host string, port int) FeatureStoreOption {
	return redisURLOption{fmt.Sprintf("redis://%s:%d", host, port)}
}

type redisPoolOption struct {
	pool *r.Pool
}

func (o redisPoolOption) apply(store *redisFeatureStoreCore) error {
	store.pool = o.pool
	return nil
}

// Pool creates an option for NewRedisFeatureStoreWithDefaults to make the feature store
// use a specific connection pool configuration. If not specified, it will create a default
// configuration (see package description). Specifying this option will cause any address
// specified with RedisURL or RedisHostAndPort to be ignored.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.Pool(myPool))
func Pool(pool *r.Pool) FeatureStoreOption {
	return redisPoolOption{pool}
}

type prefixOption struct {
	prefix string
}

func (o prefixOption) apply(store *redisFeatureStoreCore) error {
	if o.prefix == "" {
		store.prefix = DefaultPrefix
	} else {
		store.prefix = o.prefix
	}
	return nil
}

// Prefix creates an option for NewRedisFeatureStoreWithDefaults to specify a string
// that should be prepended to all Redis keys used by the feature store. A colon will be
// added to this automatically. If this is unspecified or empty, DefaultPrefix will be used.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.Prefix("ld-data"))
func Prefix(prefix string) FeatureStoreOption {
	return prefixOption{prefix}
}

type cacheTTLOption struct {
	cacheTTL time.Duration
}

func (o cacheTTLOption) apply(store *redisFeatureStoreCore) error {
	store.cacheTTL = o.cacheTTL
	return nil
}

// CacheTTL creates an option for NewRedisFeatureStoreWithDefaults to set the amount of time
// that recently read or updated items should remain in an in-memory cache. This reduces the
// amount of database access if the same feature flags are being evaluated repeatedly. If it
// is zero, there will be no in-memory caching. The default value is DefaultCacheTTL.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.CacheTTL(30*time.Second))
func CacheTTL(ttl time.Duration) FeatureStoreOption {
	return cacheTTLOption{ttl}
}

type loggerOption struct {
	logger ld.Logger
}

func (o loggerOption) apply(store *redisFeatureStoreCore) error {
	store.logger = o.logger
	return nil
}

// Logger creates an option for NewDynamoDBFeatureStore, to specify where to send log output.
// If not specified, a log.Logger is used.
//
//     store, err := redis.NewRedisFeatureStoreWithDefaults(redis.Logger(myLogger))
func Logger(logger ld.Logger) FeatureStoreOption {
	return loggerOption{logger}
}

// RedisFeatureStore is a Redis-backed feature store implementation.
type RedisFeatureStore struct { // nolint:golint // package name in type name
	wrapper *utils.FeatureStoreWrapper
}

// redisFeatureStoreCore is the internal implementation, using the simpler interface defined in
// utils.FeatureStoreCore. The FeatureStoreWrapper wraps this to add caching. The only reason that
// there is a separate RedisFeatureStore type, instead of just using the FeatureStoreWrapper itself
// as the outermost object, is a historical one: the NewRedisFeatureStore constructors had already
// been defined as returning *RedisFeatureStore rather than the interface type.
type redisFeatureStoreCore struct {
	prefix     string
	pool       *r.Pool
	redisURL   string
	cacheTTL   time.Duration
	logger     ld.Logger
	testTxHook func()
}

func newPool(url string) *r.Pool {
	pool := &r.Pool{
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

const initedKey = "$inited"

// NewRedisFeatureStoreFromUrl constructs a new Redis-backed feature store connecting to the
// specified URL. It uses a default connection pool configuration (see package description for details).
// The "prefix", "timeout", and "logger" parameters are equivalent to the Prefix, CacheTTL, and
// Logger options for NewRedisFeatureStoreWithDefaults.
//
// Deprecated: It is simpler to use NewRedisFeatureStoreWithDefaults(redis.URL(url)) and override
// any other defaults as needed.
func NewRedisFeatureStoreFromUrl(url, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	return newStoreForDeprecatedConstructors(URL(url), Prefix(prefix), CacheTTL(timeout), Logger(logger))
}

// NewRedisFeatureStoreWithPool constructs a new Redis-backed feature store with the specified
// redigo pool configuration. The "prefix", "timeout", and "logger" parameters are equivalent to
// the Prefix, CacheTTL, and Logger options for NewRedisFeatureStoreWithDefaults.
//
// Deprecated: It is simpler to use NewRedisFeatureStoreWithDefaults(redis.Pool(pool)) and override
// any other defaults as needed.
func NewRedisFeatureStoreWithPool(pool *r.Pool, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	return newStoreForDeprecatedConstructors(Pool(pool), Prefix(prefix), CacheTTL(timeout), Logger(logger))
}

// NewRedisFeatureStore constructs a new Redis-backed feature store connecting to the specified
// host and port. It uses a default connection pool configuration (see package description for details).
// The "prefix", "timeout", and "logger" parameters are equivalent to the Prefix, CacheTTL, and
// Logger options for NewRedisFeatureStoreWithDefaults.
//
// Deprecated: It is simpler to use NewRedisFeatureStoreWithDefaults(redis.HostAndPort(host, port))
// and override any other defaults as needed.
func NewRedisFeatureStore(host string, port int, prefix string, timeout time.Duration, logger ld.Logger) *RedisFeatureStore {
	return newStoreForDeprecatedConstructors(HostAndPort(host, port), Prefix(prefix), CacheTTL(timeout), Logger(logger))
}

// NewRedisFeatureStoreWithDefaults constructs a new Redis-backed feature store
//
// By default, it uses DefaultURL as the Redis address, DefaultPrefix as the prefix for all keys,
// DefaultCacheTTL as the duration for in-memory caching, and a default connection pool configuration
// (see package description for details). You may override any of these with FeatureStoreOption values
// created with RedisURL, RedisHostAndPort, RedisPool, Prefix, CacheTTL, or Logger.
func NewRedisFeatureStoreWithDefaults(options ...FeatureStoreOption) (ld.FeatureStore, error) {
	core, err := newRedisFeatureStoreInternal(options...)
	if err != nil {
		return nil, err
	}
	return utils.NewFeatureStoreWrapper(core), nil
}

func newStoreForDeprecatedConstructors(options ...FeatureStoreOption) *RedisFeatureStore {
	core, err := newRedisFeatureStoreInternal(options...)
	if err != nil {
		return nil
	}
	return &RedisFeatureStore{wrapper: utils.NewFeatureStoreWrapper(core)}
}

func newRedisFeatureStoreInternal(options ...FeatureStoreOption) (*redisFeatureStoreCore, error) {
	core := redisFeatureStoreCore{
		prefix:   DefaultPrefix,
		redisURL: DefaultURL,
		cacheTTL: DefaultCacheTTL,
	}
	for _, o := range options {
		err := o.apply(&core)
		if err != nil {
			return nil, err
		}
	}

	if core.logger == nil {
		core.logger = defaultLogger()
	}
	if core.pool == nil {
		core.logger.Printf("RedisFeatureStore: Using url: %s", core.redisURL)
		core.pool = newPool(core.redisURL)
	}

	return &core, nil
}

// Get returns an individual object of a given type from the store
func (store *RedisFeatureStore) Get(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	return store.wrapper.Get(kind, key)
}

// All returns all the objects of a given kind from the store
func (store *RedisFeatureStore) All(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {
	return store.wrapper.All(kind)
}

// Init populates the store with a complete set of versioned data
func (store *RedisFeatureStore) Init(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	return store.wrapper.Init(allData)
}

// Upsert inserts or replaces an item in the store unless there it already contains an item with an equal or larger version
func (store *RedisFeatureStore) Upsert(kind ld.VersionedDataKind, item ld.VersionedData) error {
	return store.wrapper.Upsert(kind, item)
}

// Delete removes an item of a given kind from the store
func (store *RedisFeatureStore) Delete(kind ld.VersionedDataKind, key string, version int) error {
	return store.wrapper.Delete(kind, key, version)
}

// Initialized returns whether redis contains an entry for this environment
func (store *RedisFeatureStore) Initialized() bool {
	return store.wrapper.Initialized()
}

// Actual implementation methods are below - these are called by FeatureStoreWrapper, which adds
// caching behavior if necessary.

func (store *redisFeatureStoreCore) GetCacheTTL() time.Duration {
	return store.cacheTTL
}

func (store *redisFeatureStoreCore) GetInternal(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
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

	item, jsonErr := utils.UnmarshalItem(kind, []byte(jsonStr))
	if jsonErr != nil {
		return nil, jsonErr
	}
	return item, nil
}

func (store *redisFeatureStoreCore) GetAllInternal(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {
	results := make(map[string]ld.VersionedData)

	c := store.getConn()
	defer c.Close() // nolint:errcheck

	values, err := r.StringMap(c.Do("HGETALL", store.featuresKey(kind)))

	if err != nil && err != r.ErrNil {
		return nil, err
	}

	for k, v := range values {
		item, jsonErr := utils.UnmarshalItem(kind, []byte(v))

		if jsonErr != nil {
			return nil, err
		}

		results[k] = item
	}
	return results, nil
}

// Init populates the store with a complete set of versioned data
func (store *redisFeatureStoreCore) InitInternal(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	c := store.getConn()
	defer c.Close() // nolint:errcheck

	_ = c.Send("MULTI")

	for kind, items := range allData {
		baseKey := store.featuresKey(kind)

		_ = c.Send("DEL", baseKey)

		for k, v := range items {
			data, jsonErr := json.Marshal(v)

			if jsonErr != nil {
				return jsonErr
			}

			_ = c.Send("HSET", baseKey, k, data)
		}
	}

	_ = c.Send("SET", store.initedKey(), "")

	_, err := c.Do("EXEC")

	return err
}

func (store *redisFeatureStoreCore) UpsertInternal(kind ld.VersionedDataKind, newItem ld.VersionedData) (ld.VersionedData, error) {
	baseKey := store.featuresKey(kind)
	key := newItem.GetKey()
	for {
		// We accept that we can acquire multiple connections here and defer inside loop but we don't expect many
		c := store.getConn()
		defer c.Close() // nolint:errcheck

		_, err := c.Do("WATCH", baseKey)
		if err != nil {
			return nil, err
		}

		defer c.Send("UNWATCH") // nolint:errcheck // this should always succeed

		if store.testTxHook != nil { // instrumentation for unit tests
			store.testTxHook()
		}

		oldItem, err := store.GetInternal(kind, key)

		if err != nil {
			return nil, err
		}

		if oldItem != nil && oldItem.GetVersion() >= newItem.GetVersion() {
			return oldItem, nil
		}

		data, jsonErr := json.Marshal(newItem)
		if jsonErr != nil {
			return nil, jsonErr
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
				}
			}
			return newItem, nil
		}
		return nil, err
	}
}

func (store *redisFeatureStoreCore) InitializedInternal() bool {
	c := store.getConn()
	defer c.Close() // nolint:errcheck
	inited, _ := r.Bool(c.Do("EXISTS", store.initedKey()))
	return inited
}

func (store *redisFeatureStoreCore) featuresKey(kind ld.VersionedDataKind) string {
	return store.prefix + ":" + kind.GetNamespace()
}

func (store *redisFeatureStoreCore) initedKey() string {
	return store.prefix + ":" + initedKey
}

func (store *redisFeatureStoreCore) getConn() r.Conn {
	return store.pool.Get()
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
}
