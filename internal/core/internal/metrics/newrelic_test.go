package metrics

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestNewRelicExporterType(t *testing.T) {
	exporterType := newrelicExporterType

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "Newrelic", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if Newrelic is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter if Newrelic is enabled", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Newrelic.Enabled = true
		mc.Newrelic.AppName = "sample-app"
		mc.Newrelic.InsightsKey = "insight-key"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		e.close()
	})

	t.Run("registers exporter without errors", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Newrelic.Enabled = true
		mc.Newrelic.AppName = "sample-app"
		mc.Newrelic.InsightsKey = "insight-key"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		defer e.close()
		assert.NoError(t, e.register())
	})
}
