package sdks

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-configtypes"
	ldconsul "github.com/launchdarkly/go-server-sdk-consul"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	consul "github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/assert"
)

// The unit tests for ConfigureDataStore do not actually create an SDK client or talk to a database. Instead,
// they verify that the data store builder that will be used for the SDK has been configured correctly based
// on the Relay configuration.

func assertFactoryConfigured(
	t *testing.T,
	expected interfaces.DataStoreFactory,
	expectedInfo DataStoreEnvironmentInfo,
	c config.Config,
	ec config.EnvConfig,
) *ldlogtest.MockLog {
	mockLog := ldlogtest.NewMockLog()
	factory, info, err := ConfigureDataStore(c, ec, mockLog.Loggers)
	assert.NoError(t, err)
	assert.Equal(t, expected, factory)
	assert.Equal(t, expectedInfo, info)
	return mockLog
}

func TestConfigureDataStoreDefault(t *testing.T) {
	log := assertFactoryConfigured(t, ldcomponents.InMemoryDataStore(), DataStoreEnvironmentInfo{}, config.Config{}, config.EnvConfig{})
	assert.Len(t, log.GetAllOutput(), 0)
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
		expected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(redisURL),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis data store: "+redisURL)
	})

	t.Run("password is redacted in log", func(t *testing.T) {
		urlWithPassword := "redis://username:very-secret-password@redishost:3000"
		redactedURL := "redis://username:xxxxx@redishost:3000"
		var c config.Config
		c.Redis.URL, _ = configtypes.NewOptURLAbsoluteFromString(urlWithPassword)
		expected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(urlWithPassword),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redactedURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis data store: "+redactedURL)
	})

	t.Run("prefix", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
			},
		}
		ec := config.EnvConfig{Prefix: "abc"}
		expected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(redisURL).Prefix("abc"),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: "abc"}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, ec)
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis data store: "+redisURL+" with prefix: abc")
	})

	t.Run("TTL", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL:      optRedisURL,
				LocalTTL: configtypes.NewOptDuration(time.Hour),
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(redisURL),
		).CacheTime(time.Hour)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisURL, DBPrefix: ldredis.DefaultPrefix}
		assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
	})

	t.Run("TLS", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL: optRedisURL,
				TLS: true,
			},
		}
		expected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(redisSecureURL),
		).CacheTime(config.DefaultDatabaseCacheTTL)
		expectedInfo := DataStoreEnvironmentInfo{DBType: "redis", DBServer: redisSecureURL, DBPrefix: ldredis.DefaultPrefix}
		log := assertFactoryConfigured(t, expected, expectedInfo, c, config.EnvConfig{})
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Redis data store: "+redisSecureURL)
	})

	t.Run("Password", func(t *testing.T) {
		c := config.Config{
			Redis: config.RedisConfig{
				URL:      optRedisURL,
				Password: "friend",
			},
		}
		// We won't be able to compare the data store builder for equality, because the password parameter is
		// implemented internally as a redigo *function* value. So instead we'll test for *not* being equal to
		// what the builder would look like without the password.
		notExpected := ldcomponents.PersistentDataStore(
			ldredis.DataStore().URL(redisURL),
		).CacheTime(config.DefaultDatabaseCacheTTL)

		factory, _, err := ConfigureDataStore(c, config.EnvConfig{}, ldlog.NewDisabledLoggers())
		assert.NoError(t, err)
		assert.NotEqual(t, notExpected, factory)
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
		log.AssertMessageMatch(t, true, ldlog.Info, "Using Consul data store: "+host)
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

		log.AssertMessageMatch(t, true, ldlog.Info, "Using Consul data store: "+host+" with prefix: abc")
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
	// Unfortunately, there's no good way to test the DynamoDB builder property setters, because the
	// internal configuration object that it creates has some function values inside it-- which makes
	// equality tests impossible, and there's no way to inspect the fields directly. However, our
	// unit tests and integration tests that run against a local DynamoDB instance do indirectly verify
	// that we're setting most of these properties, since otherwise those tests wouldn't work.

	t.Run("error - no table", func(t *testing.T) {
		c := config.Config{
			DynamoDB: config.DynamoDBConfig{
				Enabled: true,
			},
		}
		factory, _, err := ConfigureDataStore(c, config.EnvConfig{}, ldlog.NewDisabledLoggers())
		assert.Nil(t, factory)
		assert.Error(t, err)
	})
}
