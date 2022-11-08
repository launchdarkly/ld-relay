package sdks

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

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
) *ldlogtest.MockLog {
	mockLog := ldlogtest.NewMockLog()
	_, err := ConfigureBigSegments(c, ec, mockLog.Loggers)
	require.NoError(t, err)
	return mockLog
}

func TestBigSegmentsDefault(t *testing.T) {
	log := assertBigSegmentsConfigured(t, config.Config{}, config.EnvConfig{})
	assert.Len(t, log.GetAllOutput(), 0)
}

func TestBigSegmentsRedis(t *testing.T) {
	redisURL := "redis://redishost:3000"
	redisSecureURL := "rediss://redishost:3000"
	optRedisURL, _ := configtypes.NewOptURLAbsoluteFromString(redisURL)

	t.Run("basic properties", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisURL)
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		log := assertBigSegmentsConfigured(t, c, ec)
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisURL+" with prefix: abc")
	})

	t.Run("TLS", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
				TLS: true,
			},
		}
		log := assertBigSegmentsConfigured(t, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisSecureURL)
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
		log.AssertMessageMatch(t, true, ldlog.Info, "Using DynamoDB big segment store: "+tableName)
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
		log.AssertMessageMatch(t, true, ldlog.Info, "Using DynamoDB big segment store: "+tableName+" with prefix: abc")
	})
}
