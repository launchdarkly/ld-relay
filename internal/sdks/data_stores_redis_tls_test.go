package sdks

import (
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redigo "github.com/gomodule/redigo/redis"
)

// The data store and the big segments store dial Redis with redigo. These tests check that redigo
// uses the TLS configuration from getRedisBuilderOptions.
func TestRedisDialOptionsTLSVerification(t *testing.T) {
	files := sharedtest.NewMTLSFiles(t)
	port, _ := sharedtest.StartMTLSPingServer(t, files)

	ping := func(t *testing.T, mutate func(*config.RedisConfig)) error {
		url, err := configtypes.NewOptURLAbsoluteFromString(fmt.Sprintf("rediss://127.0.0.1:%d", port))
		require.NoError(t, err)
		c := config.Config{Redis: config.RedisConfig{
			URL:                   url,
			ClientCertificateFile: files.ClientCertFile,
			ClientKeyFile:         files.ClientKeyFile,
		}}
		mutate(&c.Redis)
		redisURL, _, dialOptions, err := getRedisBuilderOptions(c, config.EnvConfig{})
		require.NoError(t, err)
		conn, err := redigo.DialURL(redisURL, dialOptions...)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_, err = conn.Do("PING")
		return err
	}

	t.Run("fails without the CA", func(t *testing.T) {
		err := ping(t, func(*config.RedisConfig) {})
		var verifyErr *tls.CertificateVerificationError
		assert.ErrorAs(t, err, &verifyErr) // the message text differs by OS
	})

	t.Run("succeeds with the CA", func(t *testing.T) {
		assert.NoError(t, ping(t, func(c *config.RedisConfig) { c.CAFile = files.CAFile }))
	})

	t.Run("succeeds without the CA when TLSInsecureSkipVerify is set", func(t *testing.T) {
		assert.NoError(t, ping(t, func(c *config.RedisConfig) { c.TLSInsecureSkipVerify = true }))
	})
}
