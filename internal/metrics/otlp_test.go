package metrics

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOTLPExporterType(t *testing.T) {
	exporterType := otlpExporterType

	enabledConfig := func() config.MetricsConfig {
		var mc config.MetricsConfig
		mc.OTLP.Enabled = true
		mc.OTLP.Endpoint = "localhost:4317"
		mc.OTLP.Insecure = true
		return mc
	}

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "OTLP", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if OTLP is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter if OTLP is enabled", func(t *testing.T) {
		e, err := exporterType.createExporterIfEnabled(enabledConfig(), ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		assert.NoError(t, e.close())
	})

	t.Run("registers exporter without errors", func(t *testing.T) {
		e, err := exporterType.createExporterIfEnabled(enabledConfig(), ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		require.NoError(t, e.register())
		// close() forces a final metric flush; with no collector listening it returns a connection
		// error, which the exporter framework logs and tolerates. We only assert it doesn't panic.
		_ = e.close()
	})
}

func TestParseOTLPHeaders(t *testing.T) {
	assert.Nil(t, parseOTLPHeaders(""))
	assert.Nil(t, parseOTLPHeaders("   "))
	assert.Equal(t,
		map[string]string{"x-honeycomb-team": "abc123", "x-honeycomb-dataset": "ld-relay"},
		parseOTLPHeaders("x-honeycomb-team=abc123, x-honeycomb-dataset=ld-relay"),
	)
	// A value may itself contain '=', and malformed pairs without '=' are skipped.
	assert.Equal(t,
		map[string]string{"authorization": "Bearer a=b"},
		parseOTLPHeaders("authorization=Bearer a=b,garbage"),
	)
}
