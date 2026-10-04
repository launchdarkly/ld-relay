package sdks

import (
	"log/slog"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redisTLSConfig(t *testing.T, mutate func(*config.RedisConfig)) config.RedisConfig {
	url, err := configtypes.NewOptURLAbsoluteFromString("rediss://redishost:6380")
	require.NoError(t, err)
	c := config.RedisConfig{URL: url, TLS: true}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestCreateTLSConfig(t *testing.T) {
	files := sharedtest.NewMTLSFiles(t)

	t.Run("nil when TLS disabled", func(t *testing.T) {
		c := redisTLSConfig(t, func(c *config.RedisConfig) {
			c.TLS = false
			c.URL, _ = configtypes.NewOptURLAbsoluteFromString("redis://redishost:6380")
		})
		tc, err := CreateTLSConfig(c)
		assert.NoError(t, err)
		assert.Nil(t, tc)
	})

	t.Run("rediss URL enables TLS without the TLS option", func(t *testing.T) {
		c := redisTLSConfig(t, func(c *config.RedisConfig) {
			c.TLS = false
			c.CAFile = files.CAFile
		})
		tc, err := CreateTLSConfig(c)
		require.NoError(t, err)
		require.NotNil(t, tc)
		assert.NotNil(t, tc.RootCAs)
	})

	t.Run("redis URL without the TLS option stays plaintext", func(t *testing.T) {
		url, _ := configtypes.NewOptURLAbsoluteFromString("redis://redishost:6380")
		tc, err := CreateTLSConfig(config.RedisConfig{URL: url, CAFile: files.CAFile})
		assert.NoError(t, err)
		assert.Nil(t, tc)
	})

	t.Run("defaults", func(t *testing.T) {
		tc, err := CreateTLSConfig(redisTLSConfig(t, nil))
		require.NoError(t, err)
		assert.Equal(t, "redishost", tc.ServerName)
		assert.Nil(t, tc.RootCAs)
		assert.Empty(t, tc.Certificates)
	})

	t.Run("client cert, key and CA", func(t *testing.T) {
		tc, err := CreateTLSConfig(redisTLSConfig(t, func(c *config.RedisConfig) {
			c.ClientCertificateFile = files.ClientCertFile
			c.ClientKeyFile = files.ClientKeyFile
			c.CAFile = files.CAFile
		}))
		require.NoError(t, err)
		assert.Len(t, tc.Certificates, 1)
		assert.NotNil(t, tc.RootCAs)
	})

	t.Run("cert without key is ignored", func(t *testing.T) {
		tc, err := CreateTLSConfig(redisTLSConfig(t, func(c *config.RedisConfig) {
			c.ClientCertificateFile = files.ClientCertFile
		}))
		require.NoError(t, err)
		assert.Empty(t, tc.Certificates)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := CreateTLSConfig(redisTLSConfig(t, func(c *config.RedisConfig) {
			c.ClientCertificateFile = "/nonexistent.pem"
			c.ClientKeyFile = "/nonexistent.key"
		}))
		assert.Error(t, err)

		_, err = CreateTLSConfig(redisTLSConfig(t, func(c *config.RedisConfig) { c.CAFile = "/nonexistent.pem" }))
		assert.Error(t, err)

		_, err = CreateTLSConfig(redisTLSConfig(t, func(c *config.RedisConfig) {
			c.CAFile = files.ClientKeyFile // readable, but not a certificate
		}))
		assert.Error(t, err)
	})
}

func TestConfigureDataStoreRedisTLSFileErrorIsReturned(t *testing.T) {
	url, _ := configtypes.NewOptURLAbsoluteFromString("rediss://redishost:6380")
	c := config.Config{Redis: config.RedisConfig{URL: url, CAFile: "/nonexistent.pem"}}
	logger := slog.New(slog.DiscardHandler)

	_, _, err := ConfigureDataStore(c, config.EnvConfig{}, logger)
	assert.Error(t, err)

	_, err = ConfigureBigSegments(c, config.EnvConfig{}, logger)
	assert.Error(t, err)
}
