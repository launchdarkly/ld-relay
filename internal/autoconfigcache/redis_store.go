package autoconfigcache

import (
	"context"
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/go-redis/redis/v8"
)

const redisKeyPrefix = "ld:autoconfig:"

type redisStore struct {
	client   redis.UniversalClient
	fullKey  string
	encKey   []byte
	loggers  ldlog.Loggers
}

func newRedisStore(redisConfig config.RedisConfig, cacheKey string, encKey []byte, loggers ldlog.Loggers) (Store, error) {
	redisURL := redisConfig.URL.String()
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	uo := &redis.UniversalOptions{
		Addrs:     []string{opts.Addr},
		DB:        opts.DB,
		Username:  opts.Username,
		Password:  opts.Password,
		TLSConfig: opts.TLSConfig,
	}
	if redisConfig.Password != "" {
		uo.Password = redisConfig.Password
	}
	if redisConfig.Username != "" {
		uo.Username = redisConfig.Username
	}
	client := redis.NewUniversalClient(uo)
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	fullKey := redisKeyPrefix + cacheKey
	return &redisStore{client: client, fullKey: fullKey, encKey: encKey, loggers: loggers}, nil
}

func (s *redisStore) Get(ctx context.Context) ([]byte, error) {
	enc, err := s.client.Get(ctx, s.fullKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	return decrypt(enc, s.encKey)
}

func (s *redisStore) Set(ctx context.Context, value []byte) error {
	enc, err := encrypt(value, s.encKey)
	if err != nil {
		return fmt.Errorf("encrypt autoconfig cache: %w", err)
	}
	return s.client.Set(ctx, s.fullKey, enc, 0).Err()
}

func (s *redisStore) Close() error {
	return s.client.Close()
}
