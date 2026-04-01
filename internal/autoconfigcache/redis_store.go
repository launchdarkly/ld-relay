package autoconfigcache

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

type redisStore struct {
	client  redis.UniversalClient
	hashKey string
	encKey  []byte
	loggers ldlog.Loggers
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
	if redisConfig.TLS && uo.TLSConfig == nil {
		uo.TLSConfig = &tls.Config{
			ServerName: redisConfig.URL.Get().Hostname(),
			MinVersion: tls.VersionTLS12,
		}
	}
	client := redis.NewUniversalClient(uo)
	hashKey := cacheKey
	return &redisStore{client: client, hashKey: hashKey, encKey: encKey, loggers: loggers}, nil
}

func (s *redisStore) GetAll(ctx context.Context) (*autoconfig.PutContent, error) {
	fields, err := s.client.HGetAll(ctx, s.hashKey).Result()
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, nil
	}

	content := &autoconfig.PutContent{
		Environments: make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		Filters:      make(map[config.FilterID]envfactory.FilterRep),
	}

	for field, encValue := range fields {
		plaintext, err := decrypt([]byte(encValue), s.encKey)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to decrypt field %q: %v", field, err)
			continue
		}

		if strings.HasPrefix(field, envItemPrefix) {
			envID := config.EnvironmentID(strings.TrimPrefix(field, envItemPrefix))
			var rep envfactory.EnvironmentRep
			if err := json.Unmarshal(plaintext, &rep); err != nil {
				s.loggers.Warnf("AutoConfig cache: failed to unmarshal env %q: %v", envID, err)
				continue
			}
			content.Environments[envID] = rep
		} else if strings.HasPrefix(field, filterItemPrefix) {
			filterID := config.FilterID(strings.TrimPrefix(field, filterItemPrefix))
			var rep envfactory.FilterRep
			if err := json.Unmarshal(plaintext, &rep); err != nil {
				s.loggers.Warnf("AutoConfig cache: failed to unmarshal filter %q: %v", filterID, err)
				continue
			}
			content.Filters[filterID] = rep
		}
	}

	if len(content.Environments) == 0 && len(content.Filters) == 0 {
		return nil, nil
	}

	return content, nil
}

func (s *redisStore) SetAll(ctx context.Context, content autoconfig.PutContent) error {
	fields := make(map[string]interface{})

	for id, rep := range content.Environments {
		if id != rep.EnvID {
			continue
		}
		raw, err := json.Marshal(rep)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to marshal env %q: %v", id, err)
			continue
		}
		enc, err := encrypt(raw, s.encKey)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to encrypt env %q: %v", id, err)
			continue
		}
		fields[envItemPrefix+string(id)] = enc
	}

	for id, rep := range content.Filters {
		raw, err := json.Marshal(rep)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to marshal filter %q: %v", id, err)
			continue
		}
		enc, err := encrypt(raw, s.encKey)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to encrypt filter %q: %v", id, err)
			continue
		}
		fields[filterItemPrefix+string(id)] = enc
	}

	// Atomic replacement: DEL + HSET in a MULTI/EXEC transaction
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, s.hashKey)
		if len(fields) > 0 {
			pipe.HSet(ctx, s.hashKey, fields)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("AutoConfig cache redis write: %w", err)
	}
	return nil
}

func (s *redisStore) Upsert(ctx context.Context, kind autoconfig.CacheKind, id string, data interface{}) error {
	field := cacheField(kind, id)
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("AutoConfig cache: failed to marshal %q: %w", field, err)
	}
	enc, err := encrypt(raw, s.encKey)
	if err != nil {
		return fmt.Errorf("AutoConfig cache: failed to encrypt %q: %w", field, err)
	}
	return s.client.HSet(ctx, s.hashKey, field, enc).Err()
}

func (s *redisStore) Delete(ctx context.Context, kind autoconfig.CacheKind, id string) error {
	return s.client.HDel(ctx, s.hashKey, cacheField(kind, id)).Err()
}

func (s *redisStore) Close() error {
	return s.client.Close()
}
