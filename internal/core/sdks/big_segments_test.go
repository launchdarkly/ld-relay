package sdks

import (
	"testing"

	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb"
	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-configtypes"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertBigSegmentsConfigured(
	t *testing.T,
	expected interfaces.BigSegmentsConfigurationFactory,
	c config.Config,
	ec config.EnvConfig,
) *ldlogtest.MockLog {
	mockLog := ldlogtest.NewMockLog()
	factory, err := ConfigureBigSegments(c, ec, nil, mockLog.Loggers)
	require.NoError(t, err)
	assert.Equal(t, expected, factory)
	return mockLog
}

func TestBigSegmentsDefault(t *testing.T) {
	log := assertBigSegmentsConfigured(t, nil, config.Config{}, config.EnvConfig{})
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
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: ldredis.DataStore().URL(redisURL),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisURL)
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: ldredis.DataStore().URL(redisURL).Prefix("abc"),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, ec)
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisURL+" with prefix: abc")
	})

	t.Run("TLS", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
				TLS: true,
			},
		}
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: ldredis.DataStore().URL(redisSecureURL),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisSecureURL)
	})
}

func TestBigSegmentsDynamoDB(t *testing.T) {
	table := "my-table"

	t.Run("basic properties - global table name", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled:   true,
				TableName: table,
			},
		}
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: lddynamodb.DataStore(table),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using DynamoDB big segment store: "+table)
	})

	t.Run("basic properties - per-environment table name", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled: true,
			},
		}
		ec := config.EnvConfig{TableName: table}
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: lddynamodb.DataStore(table),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, ec)
		log.AssertMessageMatch(t, true, ldlog.Info, "Using DynamoDB big segment store: "+table)
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled:   true,
				TableName: table,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		expected := ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory: lddynamodb.DataStore(table).Prefix("abc"),
			},
		)
		log := assertBigSegmentsConfigured(t, expected, c, ec)
		log.AssertMessageMatch(t, true, ldlog.Info, "Using DynamoDB big segment store: "+table+" with prefix: abc")
	})
}
