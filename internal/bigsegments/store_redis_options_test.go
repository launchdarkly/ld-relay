package bigsegments

import (
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"

	"github.com/launchdarkly/go-configtypes"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisBigSegmentStoreTimeouts(t *testing.T) {
	makeOptions := func(t *testing.T, redisConfig config.RedisConfig) *redis.Options {
		redisConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString("redis://127.0.0.1:6379")
		store, err := newRedisBigSegmentStore(redisConfig, config.EnvConfig{}, false, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		client, ok := store.client.(*redis.Client)
		require.True(t, ok)
		return client.Options()
	}

	t.Run("configured timeouts are applied", func(t *testing.T) {
		options := makeOptions(t, config.RedisConfig{
			ConnectTimeout: configtypes.NewOptDuration(2 * time.Second),
			ReadTimeout:    configtypes.NewOptDuration(4 * time.Second),
		})
		assert.Equal(t, 2*time.Second, options.DialTimeout)
		assert.Equal(t, 4*time.Second, options.ReadTimeout)
	})

	t.Run("unset timeouts keep the client defaults", func(t *testing.T) {
		options := makeOptions(t, config.RedisConfig{})
		assert.Equal(t, 5*time.Second, options.DialTimeout)
		assert.Equal(t, 3*time.Second, options.ReadTimeout)
	})
}
