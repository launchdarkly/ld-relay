package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrencyConfigFromEnvironment confirms the [Concurrency] budget parses from
// the INIT_* environment variables.
func TestConcurrencyConfigFromEnvironment(t *testing.T) {
	withEnvironment(map[string]string{
		"INIT_MAX_CONCURRENT":      "200",
		"INIT_MAX_QUEUED":          "1000",
		"INIT_PER_ENV_MAX_PERCENT": "40",
		"INIT_SEND_TIMEOUT":        "15s",
	}, func() {
		var c Config
		require.NoError(t, LoadConfigFromEnvironment(&c, slog.Default()))
		assert.Equal(t, 200, c.Concurrency.MaxConcurrent.GetOrElse(-1))
		assert.Equal(t, 1000, c.Concurrency.MaxQueued.GetOrElse(-1))
		assert.Equal(t, 40, c.Concurrency.PerEnvMaxPercent.GetOrElse(-1))
		assert.Equal(t, 15*time.Second, c.Concurrency.SendTimeout.GetOrElse(0))
	})
}

// TestConcurrencyMaxConcurrentZeroDisablesGracefully is a regression guard: an explicit
// INIT_MAX_CONCURRENT=0 (a natural way to turn the feature off) must load successfully
// and leave the limiter disabled, NOT fail config validation and crashloop the Relay.
// It is an OptInt, not OptIntGreaterThanZero, precisely so 0 is accepted.
func TestConcurrencyMaxConcurrentZeroDisablesGracefully(t *testing.T) {
	withEnvironment(map[string]string{"INIT_MAX_CONCURRENT": "0"}, func() {
		var c Config
		require.NoError(t, LoadConfigFromEnvironment(&c, slog.Default()))
		assert.Equal(t, 0, c.Concurrency.MaxConcurrent.GetOrElse(-1))
	})
}
