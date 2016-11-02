package ldclient

import (
	"encoding/json"
	"fmt"
	r "github.com/garyburd/redigo/redis"
	"github.com/patrickmn/go-cache"
	"log"
	"os"
	"time"
	"reflect"
)

// A Redis-backed feature store.
type RedisFeatureStore struct {
	prefix  string
	pool    *r.Pool
	cache   *cache.Cache
	timeout time.Duration
	logger  *log.Logger
}

const (
	initKey = "$initialized$"

	// stored alongside flag keys.
	// $ is not a valid character for flag keys, so there will never be a collision.
	allFlagsKey = "$all_flags$"
)

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
func NewRedisFeatureStoreFromUrl(url, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
	if logger == nil {
		logger = defaultLogger()
	}
	logger.Printf("RedisFeatureStore: Using url: %s", url)
	return NewRedisFeatureStoreWithPool(newPool(url), prefix, timeout, logger)

}

// Constructs a new Redis-backed feature store with the specified redigo pool configuration.
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly".
func NewRedisFeatureStoreWithPool(pool *r.Pool, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
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
	}
	return &store
}

// Constructs a new Redis-backed feature store connecting to the specified host and port with a default
// connection pool configuration (16 concurrent connections, connection requests block).
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly"
func NewRedisFeatureStore(host string, port int, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
	return NewRedisFeatureStoreFromUrl(fmt.Sprintf("redis://%s:%d", host, port), prefix, timeout, logger)
}

func (store *RedisFeatureStore) featuresKey() string {
	return store.prefix + ":features"
}

func (store *RedisFeatureStore) Get(key string) (*FeatureFlag, error) {
	var feature FeatureFlag

	if store.cache != nil {
		if data, present := store.cache.Get(key); present {
			if feature, ok := data.(FeatureFlag); ok {
				if feature.Deleted {
					store.logger.Printf("RedisFeatureStore: WARN: Attempted to get deleted feature flag (from local cache). Key: %s", key)
					return nil, nil
				}
				return &feature, nil
			} else {
				store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %v. Expected FeatureFlag",
					reflect.TypeOf(data))
			}
		}
	}

	c := store.getConn()
	defer c.Close()

	jsonStr, err := r.String(c.Do("HGET", store.featuresKey(), key))

	if err != nil {
		if err == r.ErrNil {
			store.logger.Printf("RedisFeatureStore: WARN: Feature flag not found in store. Key: %s", key)
			return nil, nil
		}
		return nil, err
	}

	if jsonErr := json.Unmarshal([]byte(jsonStr), &feature); jsonErr != nil {
		return nil, jsonErr
	}

	if feature.Deleted {
		store.logger.Printf("RedisFeatureStore: WARN: Attempted to get deleted feature flag (from redis). Key: %s", key)
		return nil, nil
	}

	if store.cache != nil {
		store.cache.Set(key, feature, store.timeout)
	}

	return &feature, nil
}

func (store *RedisFeatureStore) All() (map[string]*FeatureFlag, error) {

	if store.cache != nil {
		if data, present := store.cache.Get(allFlagsKey); present {
			if features, ok := data.(map[string]*FeatureFlag); ok {
				return features, nil
			} else {
				store.logger.Printf("ERROR: RedisFeatureStore's in-memory cache returned an unexpected type: %v. Expected map[string]*FeatureFlag",
					reflect.TypeOf(data))
			}
		}
	}

	results := make(map[string]*FeatureFlag)

	c := store.getConn()
	defer c.Close()

	values, err := r.StringMap(c.Do("HGETALL", store.featuresKey()))

	if err != nil && err != r.ErrNil {
		return nil, err
	}

	for k, v := range values {
		var feature FeatureFlag
		jsonErr := json.Unmarshal([]byte(v), &feature)

		if jsonErr != nil {
			return nil, err
		}

		if !feature.Deleted {
			results[k] = &feature
		}
	}
	if store.cache != nil {
		store.cache.Set(allFlagsKey, results, store.timeout)
	}
	return results, nil
}

func (store *RedisFeatureStore) Init(features map[string]*FeatureFlag) error {
	c := store.getConn()
	defer c.Close()

	c.Send("MULTI")
	c.Send("DEL", store.featuresKey())

	if store.cache != nil {
		store.cache.Flush()
	}

	for k, v := range features {
		data, jsonErr := json.Marshal(v)

		if jsonErr != nil {
			return jsonErr
		}

		c.Send("HSET", store.featuresKey(), k, data)

		if store.cache != nil {
			store.cache.Set(k, *v, store.timeout)
		}
	}
	if store.cache != nil {
		store.cache.Set(allFlagsKey, features, store.timeout)
	}
	_, err := c.Do("EXEC")
	return err
}

func (store *RedisFeatureStore) Delete(key string, version int) error {
	c := store.getConn()
	defer c.Close()

	c.Send("WATCH", store.featuresKey())
	defer c.Send("UNWATCH")

	f, featureErr := store.Get(key)

	if featureErr != nil {
		return featureErr
	}
	if f != nil && f.Version < version {
		f.Deleted = true
		f.Version = version
		return store.put(c, key, *f)

	} else if f == nil {
		f = &FeatureFlag{Deleted: true, Version: version}
		return store.put(c, key, *f)
	}
	return nil
}

func (store *RedisFeatureStore) Upsert(key string, f FeatureFlag) error {
	c := store.getConn()
	defer c.Close()

	c.Send("WATCH", store.featuresKey())
	defer c.Send("UNWATCH")

	o, featureErr := store.Get(key)

	if featureErr != nil {
		return featureErr
	}

	if o != nil && o.Version >= f.Version {
		return nil
	}
	return store.put(c, key, f)
}

func (store *RedisFeatureStore) put(c r.Conn, key string, f FeatureFlag) error {
	data, jsonErr := json.Marshal(f)

	if jsonErr != nil {
		return jsonErr
	}

	_, err := c.Do("HSET", store.featuresKey(), key, data)

	if err == nil && store.cache != nil {
		store.cache.Delete(allFlagsKey)
		store.cache.Set(key, f, store.timeout)
	}

	return err
}

func (store *RedisFeatureStore) Initialized() bool {
	if store.cache != nil {
		if _, present := store.cache.Get(initKey); present {
			return true
		}
	}

	c := store.getConn()
	defer c.Close()

	init, err := r.Bool(c.Do("EXISTS", store.featuresKey()))

	if store.cache != nil && err == nil && init {
		store.cache.Set(initKey, true, store.timeout)
	}

	return err == nil && init
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
}
