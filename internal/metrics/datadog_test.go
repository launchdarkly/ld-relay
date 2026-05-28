package metrics

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	t.Run("ignores deprecated stats address", func(t *testing.T) {
		// DATADOG_STATS_ADDR is no longer used now that Relay ships metrics via OTLP into
		// the Datadog Agent. The exporter logs a warning and continues.
		var mc config.MetricsConfig
		mc.Datadog.Enabled = true
		mc.Datadog.StatsAddr = "127.0.0.1:8125"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		_ = e.close()
	})

	t.Run("registers exporter without errors", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Datadog.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		defer e.close()
		assert.NoError(t, e.register(ldlog.NewDisabledLoggers()))
	})
}
