package metrics

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenTelemetryExporterType(t *testing.T) {
	exporterType := otelExporterType

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "OpenTelemetry", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if OpenTelemetry is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter when enabled with defaults", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.OpenTelemetry.Enabled = true
		mc.OpenTelemetry.Insecure = true
		mc.OpenTelemetry.Endpoint = "localhost:4317"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		// close may surface connection-refused since no OTLP collector runs in unit tests.
		_ = e.close()
	})

	t.Run("creates exporter when enabled with http protocol", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.OpenTelemetry.Enabled = true
		mc.OpenTelemetry.Insecure = true
		mc.OpenTelemetry.Protocol = "http/protobuf"
		mc.OpenTelemetry.Endpoint = "http://localhost:4318"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		_ = e.close()
	})

	t.Run("rejects unknown protocol", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.OpenTelemetry.Enabled = true
		mc.OpenTelemetry.Protocol = "carrier-pigeon"
		_, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		assert.Error(t, err)
	})

	t.Run("rejects when both signals disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.OpenTelemetry.Enabled = true
		mc.OpenTelemetry.DisableTraces = true
		mc.OpenTelemetry.DisableMetrics = true
		_, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		assert.Error(t, err)
	})

	t.Run("registers without error", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.OpenTelemetry.Enabled = true
		mc.OpenTelemetry.Insecure = true
		mc.OpenTelemetry.Endpoint = "localhost:4317"
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)
		defer func() { _ = e.close() }()
		assert.NoError(t, e.register(ldlog.NewDisabledLoggers()))
	})
}

func TestParseOTLPHeaders(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		h, err := parseOTLPHeaders("")
		require.NoError(t, err)
		assert.Nil(t, h)
	})

	t.Run("single pair", func(t *testing.T) {
		h, err := parseOTLPHeaders("api-key=secret")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"api-key": "secret"}, h)
	})

	t.Run("multiple pairs trimmed", func(t *testing.T) {
		h, err := parseOTLPHeaders(" api-key = secret , tenant = acme ")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"api-key": "secret", "tenant": "acme"}, h)
	})

	t.Run("rejects malformed", func(t *testing.T) {
		_, err := parseOTLPHeaders("no-equals-sign")
		assert.Error(t, err)
	})

	t.Run("rejects empty key", func(t *testing.T) {
		_, err := parseOTLPHeaders("=value")
		assert.Error(t, err)
	})
}

func TestStripScheme(t *testing.T) {
	assert.Equal(t, "host:4317", stripScheme("http://host:4317"))
	assert.Equal(t, "host:4317", stripScheme("https://host:4317"))
	assert.Equal(t, "host:4317", stripScheme("host:4317"))
}
