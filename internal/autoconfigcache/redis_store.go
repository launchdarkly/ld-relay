package autoconfigcache

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
)

type redisStore struct {
	client  redis.UniversalClient
	ctx     context.Context
	cancel  context.CancelFunc
	hashKey string
	encKey  []byte
	logger  *slog.Logger
}

func newRedisStore(redisConfig config.RedisConfig, cacheKey string, encKey []byte, logger *slog.Logger) (Store, error) {
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
	ctx, cancel := context.WithCancel(context.Background())
	return &redisStore{client: client, ctx: ctx, cancel: cancel, hashKey: cacheKey, encKey: encKey, logger: logger}, nil
}

func (s *redisStore) GetAll(ctx context.Context) (*autoconfig.PutContent, error) {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
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
			s.logger.Warn("AutoConfig cache: failed to decrypt field", "field", field, "error", err)
			continue
		}

		item, err := unmarshalCachedItem(plaintext)
		if err != nil {
			s.logger.Warn("AutoConfig cache: skipping field", "field", field, "error", err)
			continue
		}

		switch item.Kind {
		case ModelKindEnvironment:
			envID := config.EnvironmentID(strings.TrimPrefix(field, envItemPrefix))
			var rep envfactory.EnvironmentRep
			if err := json.Unmarshal(item.Data, &rep); err != nil {
				s.logger.Warn("AutoConfig cache: failed to unmarshal env", "envId", envID, "error", err)
				continue
			}
			content.Environments[envID] = rep
		case ModelKindFilter:
			filterID := config.FilterID(strings.TrimPrefix(field, filterItemPrefix))
			var rep envfactory.FilterRep
			if err := json.Unmarshal(item.Data, &rep); err != nil {
				s.logger.Warn("AutoConfig cache: failed to unmarshal filter", "filterId", filterID, "error", err)
				continue
			}
			content.Filters[filterID] = rep
		default:
			s.logger.Warn("AutoConfig cache: skipping field with unknown kind", "field", field, "kind", item.Kind)
		}
	}

	if len(content.Environments) == 0 && len(content.Filters) == 0 {
		return nil, nil
	}

	return content, nil
}

func (s *redisStore) SetAll(ctx context.Context, content autoconfig.PutContent) error {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	fields := make(map[string]interface{})

	for id, rep := range content.Environments {
		if id != rep.EnvID {
			continue
		}
		raw, err := marshalCachedItem(ModelKindEnvironment, rep)
		if err != nil {
			s.logger.Warn("AutoConfig cache: failed to marshal env", "envId", id, "error", err)
			continue
		}
		enc, err := encrypt(raw, s.encKey)
		if err != nil {
			s.logger.Warn("AutoConfig cache: failed to encrypt env", "envId", id, "error", err)
			continue
		}
		fields[envItemPrefix+string(id)] = enc
	}

	for id, rep := range content.Filters {
		raw, err := marshalCachedItem(ModelKindFilter, rep)
		if err != nil {
			s.logger.Warn("AutoConfig cache: failed to marshal filter", "filterId", id, "error", err)
			continue
		}
		enc, err := encrypt(raw, s.encKey)
		if err != nil {
			s.logger.Warn("AutoConfig cache: failed to encrypt filter", "filterId", id, "error", err)
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
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	field := cacheField(kind, id)
	raw, err := marshalCachedItem(modelKindFromCacheKind(kind), data)
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
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	return s.client.HDel(ctx, s.hashKey, cacheField(kind, id)).Err()
}

func (s *redisStore) Close() error {
	s.cancel()
	return s.client.Close()
}
