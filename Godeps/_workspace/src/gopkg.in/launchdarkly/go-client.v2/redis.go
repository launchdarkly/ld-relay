package ldclient

import (
	"encoding/json"
	"fmt"
	"github.com/patrickmn/go-cache"
	"gopkg.in/redis.v4"
	"log"
	"os"
	"time"
)

// A Redis-backed feature store.
type RedisFeatureStore struct {
	prefix  string
	client  *redis.Client
	cache   *cache.Cache
	timeout time.Duration
	logger  *log.Logger
}

const initKey = "$initialized$"

func NewRedisFeatureStoreWithFailoverOptions(failoverOpts *redis.FailoverOptions, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
	client := redis.NewFailoverClient(failoverOpts)

	return newRedisFeatureStoreWithClient(client, prefix, timeout, logger)

}

func NewRedisFeatureStoreWithOptions(opts *redis.Options, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
	client := redis.NewClient(opts)

	return newRedisFeatureStoreWithClient(client, prefix, timeout, logger)
}

func newRedisFeatureStoreWithClient(client *redis.Client, prefix string, timeout time.Duration, logger *log.Logger) *RedisFeatureStore {
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
		client:  client,
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
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port),
		Password: "",
		DB:       0,
	}

	return NewRedisFeatureStoreWithOptions(opts, prefix, timeout, logger)
}

func (store *RedisFeatureStore) featuresKey() string {
	return store.prefix + ":features"
}

func (store *RedisFeatureStore) Get(key string) (*FeatureFlag, error) {
	return store.get(store.client, key)
}

func (store *RedisFeatureStore) All() (map[string]*FeatureFlag, error) {

	results := make(map[string]*FeatureFlag)

	values, err := store.client.HGetAll(store.featuresKey()).Result()

	if err != nil && err != redis.Nil {
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
	return results, nil
}

func (store *RedisFeatureStore) Init(features map[string]*FeatureFlag) error {
	if store.cache != nil {
		store.cache.Flush()
	}

	err := store.client.Watch(func(tx *redis.Tx) error {
		_, err := tx.MultiExec(func() error {
			for k, v := range features {
				data, jsonErr := json.Marshal(v)

				if jsonErr != nil {
					return jsonErr
				}

				tx.HSet(store.featuresKey(), k, string(data))
				if store.cache != nil {
					store.cache.Set(k, v, store.timeout)
				}
			}
			return nil
		})
		return err
	}, store.featuresKey())

	return err
}

func (store *RedisFeatureStore) Delete(key string, version int) error {
	err := store.client.Watch(func(tx *redis.Tx) error {
		f, featureErr := store.get(tx, key)

		if featureErr != nil {
			return featureErr
		}
		if f != nil && f.Version < version {
			f.Deleted = true
			f.Version = version
			_, err := store.put(tx, key, *f).Result()
			return err
		} else if f == nil {
			f = &FeatureFlag{Deleted: true, Version: version}
			_, err := store.put(tx, key, *f).Result()
			return err
		}
		return nil
	}, store.featuresKey())

	return err
}

func (store *RedisFeatureStore) Upsert(key string, f FeatureFlag) error {
	err := store.client.Watch(func(tx *redis.Tx) error {
		o, featureErr := store.get(tx, key)

		if featureErr != nil {
			return featureErr
		}

		if o != nil && o.Version >= f.Version {
			return nil
		}
		_, err := store.put(tx, key, f).Result()
		return err
	}, store.featuresKey())
	return err
}

func (store *RedisFeatureStore) Initialized() bool {
	if store.cache != nil {
		if _, present := store.cache.Get(initKey); present {
			return true
		}
	}

	init, err := store.client.Exists(store.featuresKey()).Result()

	if store.cache != nil && err == nil && init {
		store.cache.Set(initKey, true, store.timeout)
	}

	return err == nil && init
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
}

// Not safe for use in a MULTI / EXEC, but can be used within WATCH
func (store *RedisFeatureStore) get(c redis.Cmdable, key string) (*FeatureFlag, error) {
	var feature FeatureFlag
	if store.cache != nil {
		if data, present := store.cache.Get(key); present {
			if feature, ok := data.(FeatureFlag); ok {
				if feature.Deleted {
					store.logger.Printf("RedisFeatureStore: WARN: Attempted to get deleted feature flag (from local cache). Key: %s", key)
					return nil, nil
				}
				return &feature, nil
			}
		}
	}

	jsonStr, err := c.HGet(store.featuresKey(), key).Result()

	if err != nil {
		if err == redis.Nil {
			store.logger.Printf("RedisFeatureStore: WARN: Feature flag not found in store. Key: %s", key)
			return nil, nil
		}
		return nil, err
	}

	if jsonErr := json.Unmarshal([]byte(jsonStr), &feature); jsonErr != nil {
		return nil, jsonErr
	}

	if store.cache != nil {
		store.cache.Set(key, feature, store.timeout)
	}

	if feature.Deleted {
		store.logger.Printf("RedisFeatureStore: WARN: Attempted to get deleted feature flag (from redis). Key: %s", key)
		return nil, nil
	}

	return &feature, nil
}

// Safe for use in MULTI/EXEC
func (store *RedisFeatureStore) put(c redis.Cmdable, key string, f FeatureFlag) *redis.BoolCmd {
	data, _ := json.Marshal(f)

	cmd := c.HSet(store.featuresKey(), key, string(data))

	if store.cache != nil {
		store.cache.Set(key, f, store.timeout)
	}

	return cmd
}
