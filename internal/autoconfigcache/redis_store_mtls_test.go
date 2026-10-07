package autoconfigcache

import (
	"context"
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

func TestRedisStoreMTLS(t *testing.T) {
	files := sharedtest.NewMTLSFiles(t)
	port, handshakes := sharedtest.StartMTLSPingServer(t, files)
	logger := slog.New(slog.DiscardHandler)

	makeStore := func(t *testing.T, scheme string, mutate func(*config.RedisConfig)) Store {
		url, err := configtypes.NewOptURLAbsoluteFromString(fmt.Sprintf("%s://127.0.0.1:%d", scheme, port))
		require.NoError(t, err)
		c := config.RedisConfig{URL: url}
		mutate(&c)
		store, err := newRedisStore(c, "cache-key", make([]byte, 32), logger)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	withFiles := func(c *config.RedisConfig) {
		c.CAFile = files.CAFile
		c.ClientCertificateFile = files.ClientCertFile
		c.ClientKeyFile = files.ClientKeyFile
	}
	getAll := func(store Store) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := store.GetAll(ctx)
		return err
	}
	awaitHandshake := func(t *testing.T) error {
		select {
		case err := <-handshakes:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("no TLS handshake observed")
			return nil
		}
	}

	t.Run("redis URL with TLS option uses CA and client cert", func(t *testing.T) {
		store := makeStore(t, "redis", func(c *config.RedisConfig) { c.TLS = true; withFiles(c) })
		_ = getAll(store)
		assert.NoError(t, awaitHandshake(t))
	})

	t.Run("rediss URL uses CA and client cert without the TLS option", func(t *testing.T) {
		store := makeStore(t, "rediss", withFiles)
		_ = getAll(store)
		assert.NoError(t, awaitHandshake(t))
	})

	t.Run("fails without the CA", func(t *testing.T) {
		store := makeStore(t, "rediss", func(c *config.RedisConfig) {
			c.ClientCertificateFile = files.ClientCertFile
			c.ClientKeyFile = files.ClientKeyFile
		})
		err := getAll(store)
		require.Error(t, err)
		var verifyErr *tls.CertificateVerificationError
		assert.ErrorAs(t, err, &verifyErr) // the message text differs by OS
	})
}
