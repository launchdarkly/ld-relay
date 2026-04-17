package sdks

import (
	"log/slog"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/launchdarkly/go-configtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unfortunately, there's no good way to test the Redis or DynamoDB builder property setters, because
// the internal configuration objects that they create have some function values inside them-- which
// makes equality tests impossible, and there's no way to inspect the fields directly. However, our
// unit tests and integration tests that run against a local Redis/DynamoDB instance do indirectly
// verify that we're setting most of these properties, since otherwise those tests wouldn't work.

func assertBigSegmentsConfigured(
	t *testing.T,
	c config.Config,
	ec config.EnvConfig,
) *logtest.MockHandler {
	logger, handler := logtest.NewMockLogger()
	_, err := ConfigureBigSegments(c, ec, logger)
	require.NoError(t, err)
	return handler
}

func TestBigSegmentsDefault(t *testing.T) {
	log := assertBigSegmentsConfigured(t, config.Config{}, config.EnvConfig{})
	assert.Empty(t, log.AllMessages())
}

func TestBigSegmentsRedis(t *testing.T) {
	redisURL := "redis://redishost:3000"
	optRedisURL, _ := configtypes.NewOptURLAbsoluteFromString(redisURL)

	t.Run("basic properties", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis big segment store"))
	})

	t.Run("password is redacted in log", func(t *testing.T) {
		urlWithPassword := "redis://username:very-secret-password@redishost:3000"
		var c config.Config
		c.Redis.URL, _ = configtypes.NewOptURLAbsoluteFromString(urlWithPassword)
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		assert.False(t, log.HasMessage(slog.LevelInfo, "very-secret-password"))
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		log := assertBigSegmentsConfigured(t, c, ec)
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis big segment store"))
	})

	t.Run("TLS", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
				TLS: true,
			},
		}
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis big segment store"))
	})
}

func TestBigSegmentsDynamoDB(t *testing.T) {
	tableName := "my-table"

	t.Run("basic properties", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled:   true,
				TableName: tableName,
			},
		}
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using DynamoDB big segment store"))
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled:   true,
				TableName: tableName,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		log := assertBigSegmentsConfigured(t, c, ec)
		assert.True(t, log.HasMessage(slog.LevelInfo, "using DynamoDB big segment store"))
	})
}
