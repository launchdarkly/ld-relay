package sdks

import (
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/launchdarkly/go-configtypes"
	ldconsul "github.com/launchdarkly/go-server-sdk-consul/v3"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v3"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	consul "github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/assert"
)

// The unit tests for ConfigureDataStore do not actually create an SDK client or talk to a database. Instead,
// they verify that the data store builder that will be used for the SDK has been configured correctly based
// on the Relay configuration.

// Unfortunately, there's no good way to test the Redis or DynamoDB builder property setters, because
// the internal configuration objects that they create have some function values inside them-- which
// makes equality tests impossible, and there's no way to inspect the fields directly. However, our
// unit tests and integration tests that run against a local Redis/DynamoDB instance do indirectly
// verify that we're setting most of these properties, since otherwise those tests wouldn't work.

func assertFactoryConfigured(
	t *testing.T,
	expected subsystems.ComponentConfigurer[subsystems.DataStore],
	expectedInfo DataStoreEnvironmentInfo,
	c config.Config,
	ec config.EnvConfig,
) *logtest.MockHandler {
	logger, handler := logtest.NewMockLogger()
	factory, info, err := ConfigureDataStore(c, ec, logger)
	assert.NoError(t, err)
	if expected != nil {
		assert.Equal(t, expected, factory)
	}
	assert.Equal(t, expectedInfo, info)
	return handler
}

func TestConfigureDataStoreDefault(t *testing.T) {
	log := assertFactoryConfigured(t, nil, DataStoreEnvironmentInfo{}, config.Config{}, config.EnvConfig{})
	assert.Empty(t, log.AllMessages())
}

func TestConfigureDataStoreRedis(t *testing.T) {
	redisURL := "redis://redishost:3000"
	redisSecureURL := "rediss://redishost:3000"
	optRedisURL, _ := configtypes.NewOptURLAbsoluteFromString(redisURL)

	t.Run("basic properties", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, nil, expectedInfo, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis data store"))
	})

	t.Run("password is redacted in log", func(t *testing.T) {
		urlWithPassword := "redis://username:very-secret-password@redishost:3000"
		redactedURL := "redis://username:xxxxx@redishost:3000"
		var c config.Config
		c.Redis.URL, _ = configtypes.NewOptURLAbsoluteFromString(urlWithPassword)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redactedURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, nil, expectedInfo, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis data store"))
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: "abc"}
		log := assertFactoryConfigured(t, nil, expectedInfo, c, ec)
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis data store"))
	})

	t.Run("TTL", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL:      optRedisURL,
				LocalTTL: configtypes.NewOptDuration(time.Hour),
			},
		}
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: ldredis.DefaultPrefix}
		assertFactoryConfigured(t, nil, expectedInfo, c, config.EnvConfig{})
	})

	t.Run("TLS", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
				TLS: true,
			},
		}
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisSecureURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, nil, expectedInfo, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Redis data store"))
	})
}

func TestConfigureDataStoreConsul(t *testing.T) {
	host := "my-host"

	t.Run("basic properties", func(t *testing.T) {
		c := config.Config{
			Consul: config.ConsulConfig{
				Host: host,
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldconsul.DataStore().Address(host),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "consul", DBServer: host, DBPrefix: ldconsul.DefaultPrefix}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
		assert.True(t, log.HasMessage(slog.LevelInfo, "using Consul data store"))
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Consul: config.ConsulConfig{
				Host: host,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		expected := ldcomponents.PersistentDataStore(
			ldconsul.DataStore().Address(host).Prefix("abc"),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "consul", DBServer: host, DBPrefix: "abc"}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, ec)

		assert.True(t, log.HasMessage(slog.LevelInfo, "using Consul data store"))
	})

	t.Run("TTL", func(t *testing.T) {
		c := config.Config{
			Consul: config.ConsulConfig{
				Host:     host,
				LocalTTL: configtypes.NewOptDuration(time.Hour),
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldconsul.DataStore().Address(host),
		).CacheTime(time.Hour)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "consul", DBServer: host, DBPrefix: ldconsul.DefaultPrefix}
		assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
	})

	t.Run("token", func(t *testing.T) {
		c := config.Config{
			Consul: config.ConsulConfig{
				Host:  host,
				Token: "abc",
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldconsul.DataStore().Config(consul.Config{
				Address: host,
				Token:   "abc",
			}),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "consul", DBServer: host, DBPrefix: ldconsul.DefaultPrefix}
		assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
	})

	t.Run("tokenFile", func(t *testing.T) {
		c := config.Config{
			Consul: config.ConsulConfig{
				Host:      host,
				TokenFile: "def",
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldconsul.DataStore().Config(consul.Config{
				Address:   host,
				TokenFile: "def",
			}),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "consul", DBServer: host, DBPrefix: ldconsul.DefaultPrefix}
		assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
	})
}

func TestConfigureDataStoreDynamoDB(t *testing.T) {
	t.Run("error - no table", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled: true,
			},
		}
		factory, _, err := ConfigureDataStore(c, config.EnvConfig{}, slog.New(slog.DiscardHandler))
		assert.Nil(t, factory)
		assert.Error(t, err)
	})
}
