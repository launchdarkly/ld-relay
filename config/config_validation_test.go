package config

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"
)

func TestValidateMetricsCapacity(t *testing.T) {
	t.Run("unset value is left undefined", func(t *testing.T) {
		var c Config
		logger, mockHandler := logtest.NewMockLogger()
		require.NoError(t, ValidateConfig(&c, logger))
		assert.False(t, c.Events.MetricsCapacity.IsDefined())
		assert.Empty(t, mockHandler.Messages(slog.LevelWarn))
	})

	t.Run("value at or above the minimum is left unchanged", func(t *testing.T) {
		var c Config
		c.Events.MetricsCapacity = mustOptIntGreaterThanZero(2000)
		logger, mockHandler := logtest.NewMockLogger()
		require.NoError(t, ValidateConfig(&c, logger))
		assert.Equal(t, 2000, c.Events.MetricsCapacity.GetOrElse(0))
		assert.Empty(t, mockHandler.Messages(slog.LevelWarn))
	})

	t.Run("value below the minimum is clamped up and warns", func(t *testing.T) {
		var c Config
		c.Events.MetricsCapacity = mustOptIntGreaterThanZero(500)
		logger, mockHandler := logtest.NewMockLogger()
		require.NoError(t, ValidateConfig(&c, logger))
		assert.Equal(t, minimumMetricsCapacity, c.Events.MetricsCapacity.GetOrElse(0))
		assert.True(t, mockHandler.HasMessage(slog.LevelWarn,
			"usage metrics event capacity of 500 is below the minimum of 1000"))
	})
}
