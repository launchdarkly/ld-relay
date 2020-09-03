package metrics

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusExporterType(t *testing.T) {
	exporterType := prometheusExporterType

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "Prometheus", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if Prometheus is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter if Prometheus is enabled", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		e.close()
	})

	// There's currently no way to make prometheus.NewExporter fail for bad options

	t.Run("registers exporter without errors", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.NotNil(t, e)
		defer e.close()
		assert.NoError(t, e.register())
	})

	verifyPrometheusEndpointIsReachable := func(t *testing.T, port int, timeout time.Duration) {
		url := fmt.Sprintf("http://localhost:%d/metrics", port)
		require.Eventually(
			t,
			func() bool {
				resp, err := http.DefaultClient.Get(url)
				if resp != nil {
					defer resp.Body.Close()
				}
				return err == nil && resp != nil && resp.StatusCode == 200
			},
			timeout,
			time.Millisecond*10,
			"did not detect listener on port %d within %s", port, timeout,
		)
	}

	t.Run("listens on default port", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)

		defer e.close()
		require.NoError(t, e.register())

		verifyPrometheusEndpointIsReachable(t, config.DefaultPrometheusPort, time.Second)
	})

	t.Run("listens on custom port", func(t *testing.T) {
		availablePort := st.GetAvailablePort(t)
		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		mc.Prometheus.Port, _ = ct.NewOptIntGreaterThanZero(availablePort)
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)

		defer e.close()
		require.NoError(t, e.register())

		verifyPrometheusEndpointIsReachable(t, availablePort, time.Second)
	})

	t.Run("returns error if port is unavailable", func(t *testing.T) {
		st.WithListenerForAnyPort(t, func(l net.Listener, usedPort int) {
			var mc config.MetricsConfig
			mc.Prometheus.Enabled = true
			mc.Prometheus.Port, _ = ct.NewOptIntGreaterThanZero(usedPort)
			e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
			require.NoError(t, err)
			require.NotNil(t, e)

			defer e.close()
			assert.Error(t, e.register())
		})
	})
}
