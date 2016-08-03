package redis

import (
	"encoding/json"
	"fmt"
	r "github.com/garyburd/redigo/redis"
	ld "github.com/launchdarkly/go-client"
	"github.com/patrickmn/go-cache"
	"time"
)

// A Redis-backed feature store.
type RedisFeatureStore struct {
	prefix  string
	pool    *r.Pool
	cache   *cache.Cache
	timeout time.Duration
}

const initKey = "$initialized$"

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
func NewRedisFeatureStoreFromUrl(url, prefix string, timeout time.Duration) *RedisFeatureStore {
	return NewRedisFeatureStoreWithPool(newPool(url), prefix, timeout)

}

// Constructs a new Redis-backed feature store with the specified redigo pool configuration.
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly".
func NewRedisFeatureStoreWithPool(pool *r.Pool, prefix string, timeout time.Duration) *RedisFeatureStore {
	var c *cache.Cache

	if prefix == "" {
		prefix = "launchdarkly"
	}

	if timeout > 0 {
		c = cache.New(timeout, 5*time.Minute)
	}

	store := RedisFeatureStore{
		prefix:  prefix,
		pool:    pool,
		cache:   c,
		timeout: timeout,
	}

	return &store
}

// Constructs a new Redis-backed feature store connecting to the specified host and port with a default
// connection pool configuration (16 concurrent connections, connection requests block).
// Attaches a prefix string to all keys to namespace LaunchDarkly-specific keys. If the
// specified prefix is the empty string, it defaults to "launchdarkly"
func NewRedisFeatureStore(host string, port int, prefix string, timeout time.Duration) *RedisFeatureStore {
	return NewRedisFeatureStoreFromUrl(fmt.Sprintf("redis://%s:%d", host, port), prefix, timeout)
}

func (store *RedisFeatureStore) featuresKey() string {
	return store.prefix + ":features"
}

func (store *RedisFeatureStore) Get(key string) (*ld.Feature, error) {
	var feature ld.Feature

	if store.cache != nil {
		if data, present := store.cache.Get(key); present {
			if feature, ok := data.(ld.Feature); ok {
				if feature.Deleted {
					return nil, nil
				}
				return &feature, nil
			}
		}
	}

	c := store.getConn()
	defer c.Close()

	jsonStr, err := r.String(c.Do("HGET", store.featuresKey(), key))

	if err != nil {
		if err == r.ErrNil {
			return nil, nil
		}
		return nil, err
	}

	if jsonErr := json.Unmarshal([]byte(jsonStr), &feature); jsonErr != nil {
		return nil, jsonErr
	}

	if feature.Deleted {
		return nil, nil
	}

	if store.cache != nil {
		store.cache.Set(key, feature, store.timeout)
	}

	return &feature, nil
}

func (store *RedisFeatureStore) All() (map[string]*ld.Feature, error) {
	var feature ld.Feature

	results := make(map[string]*ld.Feature)

	c := store.getConn()
	defer c.Close()

	values, err := r.StringMap(c.Do("HGETALL", store.featuresKey()))

	if err != nil && err != r.ErrNil {
		return nil, err
	}

	for k, v := range values {
		jsonErr := json.Unmarshal([]byte(v), &feature)

		if jsonErr != nil {
			return nil, err
		}

		if !feature.Deleted {
			results[k] = &feature
		}
	}
	return results, nil
}

func (store *RedisFeatureStore) Init(features map[string]*ld.Feature) error {
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
			store.cache.Set(k, v, store.timeout)
		}

	}
	_, err := c.Do("EXEC")
	return err
}

func (store *RedisFeatureStore) Delete(key string, version int) error {
	c := store.getConn()
	defer c.Close()

	c.Send("WATCH", store.featuresKey())
	defer c.Send("UNWATCH")

	feature, featureErr := store.Get(key)

	if featureErr != nil {
		return featureErr
	}

	if feature != nil && feature.Version >= version {
		return nil
	}

	feature.Deleted = true
	feature.Version = version

	data, jsonErr := json.Marshal(feature)

	if jsonErr != nil {
		return jsonErr
	}

	_, err := c.Do("HSET", store.featuresKey(), key, data)

	if err == nil && store.cache != nil {
		store.cache.Set(key, feature, store.timeout)
	}

	return err
}

func (store *RedisFeatureStore) Upsert(key string, f ld.Feature) error {
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

	data, jsonErr := json.Marshal(f)

	if jsonErr != nil {
		return jsonErr
	}

	_, err := c.Do("HSET", store.featuresKey(), key, data)

	if err == nil && store.cache != nil {
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
