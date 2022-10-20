package sdks

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v2"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertBigSegmentsConfigured(
	t *testing.T,
	expected subsystems.BigSegmentsConfigurationFactory,
	c config.Config,
	ec config.EnvConfig,
) *ldlogtest.MockLog {
	mockLog := ldlogtest.NewMockLog()
	factory, err := ConfigureBigSegments(c, ec, mockLog.Loggers)
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
		expected := ldcomponents.BigSegments(ldredis.DataStore().URL(redisURL))
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
		expected := ldcomponents.BigSegments(ldredis.DataStore().URL(redisURL).Prefix("abc"))
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
		expected := ldcomponents.BigSegments(ldredis.DataStore().URL(redisSecureURL))
		log := assertBigSegmentsConfigured(t, expected, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis big segment store: "+redisSecureURL)
	})
}

// Unfortunately, there's no good way to test the DynamoDB builder property setters, because the
// internal configuration object that it creates has some function values inside it-- which makes
// equality tests impossible, and there's no way to inspect the fields directly. However, our
// unit tests and integration tests that run against a local DynamoDB instance do indirectly verify
// that we're setting most of these properties, since otherwise those tests wouldn't work.
