package bigsegments

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression test: with REDIS_TLS=true the URL is rewritten to rediss://, and the parsed default TLS
// config must not mask the configured CA and client certificate.
func TestRedisBigSegmentStoreMTLS(t *testing.T) {
	files := sharedtest.NewMTLSFiles(t)
	port, handshakes := sharedtest.StartMTLSPingServer(t, files)

	makeConfig := func(mutate func(*config.RedisConfig)) config.RedisConfig {
		url, err := configtypes.NewOptURLAbsoluteFromString(fmt.Sprintf("redis://127.0.0.1:%d", port))
		require.NoError(t, err)
		c := config.RedisConfig{URL: url, TLS: true}
		mutate(&c)
		return c
	}
	logger := slog.New(slog.DiscardHandler)

	t.Run("succeeds with CA and client cert", func(t *testing.T) {
		store, err := newRedisBigSegmentStore(makeConfig(func(c *config.RedisConfig) {
			c.CAFile = files.CAFile
			c.ClientCertificateFile = files.ClientCertFile
			c.ClientKeyFile = files.ClientKeyFile
		}), config.EnvConfig{}, true, logger)
		require.NoError(t, err)
		defer store.Close()
		assert.NoError(t, awaitHandshake(t, handshakes))
	})

	t.Run("fails without the CA", func(t *testing.T) {
		_, err := newRedisBigSegmentStore(makeConfig(func(c *config.RedisConfig) {
			c.ClientCertificateFile = files.ClientCertFile
			c.ClientKeyFile = files.ClientKeyFile
		}), config.EnvConfig{}, true, logger)
		require.Error(t, err)
		var verifyErr *tls.CertificateVerificationError
		assert.ErrorAs(t, err, &verifyErr) // the message text differs by OS
	})

	t.Run("succeeds without the CA when TLSInsecureSkipVerify is set", func(t *testing.T) {
		store, err := newRedisBigSegmentStore(makeConfig(func(c *config.RedisConfig) {
			c.ClientCertificateFile = files.ClientCertFile
			c.ClientKeyFile = files.ClientKeyFile
			c.TLSInsecureSkipVerify = true
		}), config.EnvConfig{}, true, logger)
		require.NoError(t, err)
		defer store.Close()
		awaitSuccessfulHandshake(t, handshakes)
	})

	t.Run("fails without a client cert", func(t *testing.T) {
		_, err := newRedisBigSegmentStore(makeConfig(func(c *config.RedisConfig) {
			c.CAFile = files.CAFile
		}), config.EnvConfig{}, true, logger)
		require.Error(t, err)
	})
}

func awaitHandshake(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("no TLS handshake observed")
		return nil
	}
}

// awaitSuccessfulHandshake waits for a handshake that succeeds. It skips the errors from handshakes
// that earlier subtests made fail on purpose, because those errors can arrive late.
func awaitSuccessfulHandshake(t *testing.T, ch <-chan error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var lastErr error
	for {
		select {
		case err := <-ch:
			if err == nil {
				return
			}
			lastErr = err
		case <-deadline:
			t.Fatalf("no successful TLS handshake observed (last error: %v)", lastErr)
		}
	}
}
