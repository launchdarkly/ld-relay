package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
)

func TestValidateMetricsCapacity(t *testing.T) {
	t.Run("unset value is left undefined", func(t *testing.T) {
		var c Config
		mockLog := ldlogtest.NewMockLog()
		require.NoError(t, ValidateConfig(&c, mockLog.Loggers))
		assert.False(t, c.Events.MetricsCapacity.IsDefined())
		assert.Len(t, mockLog.GetOutput(ldlog.Warn), 0)
	})

	t.Run("value at or above the minimum is left unchanged", func(t *testing.T) {
		var c Config
		c.Events.MetricsCapacity = mustOptIntGreaterThanZero(2000)
		mockLog := ldlogtest.NewMockLog()
		require.NoError(t, ValidateConfig(&c, mockLog.Loggers))
		assert.Equal(t, 2000, c.Events.MetricsCapacity.GetOrElse(0))
		assert.Len(t, mockLog.GetOutput(ldlog.Warn), 0)
	})

	t.Run("value below the minimum is clamped up and warns", func(t *testing.T) {
		var c Config
		c.Events.MetricsCapacity = mustOptIntGreaterThanZero(500)
		mockLog := ldlogtest.NewMockLog()
		require.NoError(t, ValidateConfig(&c, mockLog.Loggers))
		assert.Equal(t, minimumMetricsCapacity, c.Events.MetricsCapacity.GetOrElse(0))
		mockLog.AssertMessageMatch(t, true, ldlog.Warn, "usage metrics event capacity of 500 is below the minimum of 1000")
	})
}
