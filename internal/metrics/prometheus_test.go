package metrics

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
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
		tick := time.NewTicker(time.Millisecond * 10)
		defer tick.Stop()
		deadline := time.After(timeout)
		for {
			select {
			case <-tick.C:
				resp, err := http.DefaultClient.Get(url)
				if err == nil {
					defer resp.Body.Close()
					if resp.StatusCode == 200 {
						return
					}
				}
			case <-deadline:
				assert.Fail(t, fmt.Sprintf("did not detect listener on port %d within %s", port, timeout))
				return
			}
		}
	}

	t.Run("listens on default port", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		require.NotNil(t, e)

		defer e.close()
		require.NoError(t, e.register())

		verifyPrometheusEndpointIsReachable(t, defaultPrometheusPort, time.Second)
	})

	t.Run("listens on custom port", func(t *testing.T) {
		availablePort := 10000
		for {
			listener, err := net.Listen("tcp", fmt.Sprintf(":%d", availablePort))
			if err == nil {
				listener.Close()
				break
			}
			availablePort++
		}

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
}
