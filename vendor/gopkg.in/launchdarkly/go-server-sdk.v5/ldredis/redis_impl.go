package ldredis

import (
	"time"

	r "github.com/garyburd/redigo/redis"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// Internal implementation of the PersistentDataStore interface for Redis.
type redisDataStoreImpl struct {
	prefix     string
	pool       *r.Pool
	loggers    ldlog.Loggers
	testTxHook func()
}

func newPool(url string, dialOptions []r.DialOption) *r.Pool {
	pool := &r.Pool{
		MaxIdle:     20,
		MaxActive:   16,
		Wait:        true,
		IdleTimeout: 300 * time.Second,
		Dial: func() (c r.Conn, err error) {
			c, err = r.DialURL(url, dialOptions...)
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

func newRedisDataStoreImpl(
	builder *DataStoreBuilder,
	loggers ldlog.Loggers,
) *redisDataStoreImpl {
	impl := &redisDataStoreImpl{
		prefix:  builder.prefix,
		pool:    builder.pool,
		loggers: loggers,
	}
	impl.loggers.SetPrefix("RedisDataStore:")

	if impl.pool == nil {
		impl.loggers.Infof("Using url: %s", builder.url)
		impl.pool = newPool(builder.url, builder.dialOptions)
	}
	return impl
}

func (store *redisDataStoreImpl) Init(allData []interfaces.StoreSerializedCollection) error {
	c := store.getConn()
	defer c.Close() // nolint:errcheck

	_ = c.Send("MULTI")

	for _, coll := range allData {
		baseKey := store.featuresKey(coll.Kind)

		_ = c.Send("DEL", baseKey)

		for _, keyedItem := range coll.Items {
			_ = c.Send("HSET", baseKey, keyedItem.Key, keyedItem.Item.SerializedItem)
		}
	}

	_ = c.Send("SET", store.initedKey(), "")

	_, err := c.Do("EXEC")

	return err
}

func (store *redisDataStoreImpl) Get(
	kind interfaces.StoreDataKind,
	key string,
) (interfaces.StoreSerializedItemDescriptor, error) {
	c := store.getConn()
	defer c.Close() // nolint:errcheck

	jsonStr, err := r.String(c.Do("HGET", store.featuresKey(kind), key))

	if err != nil {
		if err == r.ErrNil {
			if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
				store.loggers.Debugf("Key: %s not found in \"%s\"", key, kind.GetName())
			}
			return interfaces.StoreSerializedItemDescriptor{}.NotFound(), nil
		}
		return interfaces.StoreSerializedItemDescriptor{}.NotFound(), err
	}

	return interfaces.StoreSerializedItemDescriptor{Version: 0, SerializedItem: []byte(jsonStr)}, nil
}

func (store *redisDataStoreImpl) GetAll(
	kind interfaces.StoreDataKind,
) ([]interfaces.StoreKeyedSerializedItemDescriptor, error) {
	c := store.getConn()
	defer c.Close() // nolint:errcheck

	values, err := r.StringMap(c.Do("HGETALL", store.featuresKey(kind)))

	if err != nil && err != r.ErrNil {
		return nil, err
	}

	results := make([]interfaces.StoreKeyedSerializedItemDescriptor, 0, len(values))
	for k, v := range values {
		results = append(results, interfaces.StoreKeyedSerializedItemDescriptor{
			Key:  k,
			Item: interfaces.StoreSerializedItemDescriptor{Version: 0, SerializedItem: []byte(v)},
		})
	}
	return results, nil
}

func (store *redisDataStoreImpl) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	newItem interfaces.StoreSerializedItemDescriptor,
) (bool, error) {
	baseKey := store.featuresKey(kind)
	for {
		// We accept that we can acquire multiple connections here and defer inside loop but we don't expect many
		c := store.getConn()
		defer c.Close() // nolint:errcheck

		_, err := c.Do("WATCH", baseKey)
		if err != nil {
			return false, err
		}

		defer c.Send("UNWATCH") // nolint:errcheck // this should always succeed

		if store.testTxHook != nil { // instrumentation for unit tests
			store.testTxHook()
		}

		oldItem, err := store.Get(kind, key)
		if err != nil { // COVERAGE: can't cause an error here in unit tests
			return false, err
		}

		// In this implementation, we have to parse the existing item in order to determine its version.
		oldVersion := oldItem.Version
		if oldItem.SerializedItem != nil {
			parsed, _ := kind.Deserialize(oldItem.SerializedItem)
			oldVersion = parsed.Version
		}

		if oldVersion >= newItem.Version {
			updateOrDelete := "update"
			if newItem.Deleted {
				updateOrDelete = "delete"
			}
			if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
				store.loggers.Debugf(`Attempted to %s key: %s version: %d in "%s" with a version that is the same or older: %d`,
					updateOrDelete, key, oldVersion, kind, newItem.Version)
			}
			return false, nil
		}

		_ = c.Send("MULTI")
		err = c.Send("HSET", baseKey, key, newItem.SerializedItem)
		if err == nil {
			var result interface{}
			result, err = c.Do("EXEC")
			if err == nil {
				if result == nil {
					// if exec returned nothing, it means the watch was triggered and we should retry
					if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
						store.loggers.Debug("Concurrent modification detected, retrying")
					}
					continue
				}
			}
			return true, nil
		}
		return false, err // COVERAGE: can't cause an error here in unit tests
	}
}

func (store *redisDataStoreImpl) IsInitialized() bool {
	c := store.getConn()
	defer c.Close() // nolint:errcheck
	inited, _ := r.Bool(c.Do("EXISTS", store.initedKey()))
	return inited
}

func (store *redisDataStoreImpl) IsStoreAvailable() bool {
	c := store.getConn()
	defer c.Close() // nolint:errcheck
	_, err := r.Bool(c.Do("EXISTS", store.initedKey()))
	return err == nil
}

func (store *redisDataStoreImpl) Close() error {
	return store.pool.Close()
}

func (store *redisDataStoreImpl) featuresKey(kind interfaces.StoreDataKind) string {
	return store.prefix + ":" + kind.GetName()
}

func (store *redisDataStoreImpl) initedKey() string {
	return store.prefix + ":" + initedKey
}

func (store *redisDataStoreImpl) getConn() r.Conn {
	return store.pool.Get()
}
