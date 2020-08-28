package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestDatadogExporterType(t *testing.T) {
	exporterType := datadogExporterType

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "Datadog", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if Datadog is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter if Datadog is enabled", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Datadog.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		e.close()
	})

	t.Run("returns error for invalid stats address", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Datadog.Enabled = true
		mc.Datadog.StatsAddr = "::"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.Error(t, err)
		assert.Nil(t, e)
	})

	t.Run("registers exporter without errors", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Datadog.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		defer e.close()
		assert.NoError(t, e.register())
	})
}
