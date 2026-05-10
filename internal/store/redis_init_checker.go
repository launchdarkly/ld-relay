package store

import (
	"fmt"

	redigo "github.com/gomodule/redigo/redis"
)

// RedisInitChecker implements StoreInitChecker by directly querying Redis for the
// $inited sentinel key, bypassing the SDK's caching layer.
type RedisInitChecker struct {
	pool   *redigo.Pool
	prefix string
}

// NewRedisInitChecker creates a checker that connects to Redis using the given URL and
// dial options. The prefix should match the store prefix used by the SDK (e.g. "launchdarkly").
func NewRedisInitChecker(redisURL string, prefix string, dialOptions []redigo.DialOption) *RedisInitChecker {
	pool := &redigo.Pool{
		MaxIdle:   1,
		MaxActive: 1,
		Dial: func() (redigo.Conn, error) {
			return redigo.DialURL(redisURL, dialOptions...)
		},
	}
	return &RedisInitChecker{
		pool:   pool,
		prefix: prefix,
	}
}

func (r *RedisInitChecker) initedKey() string {
	return fmt.Sprintf("%s:$inited", r.prefix)
}

// CheckInitialized checks if the $inited key exists in Redis.
func (r *RedisInitChecker) CheckInitialized() (available bool, initialized bool, err error) {
	conn := r.pool.Get()
	defer conn.Close() //nolint:errcheck

	exists, err := redigo.Bool(conn.Do("EXISTS", r.initedKey()))
	if err != nil {
		return false, false, err
	}
	return true, exists, nil
}

// Close releases the Redis connection pool.
func (r *RedisInitChecker) Close() error {
	return r.pool.Close()
}
