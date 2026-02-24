package metrics

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusExporter(t *testing.T) {
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

	t.Run("does not create exporter if Prometheus is disabled", func(t *testing.T) {
		mc := config.MetricsConfig{}
		manager, err := NewManager(mc, 0, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		defer manager.Close()
		assert.Nil(t, manager.prometheusServer)
	})

	t.Run("creates exporter if Prometheus is enabled", func(t *testing.T) {
		availablePort := st.GetAvailablePort(t)
		mc := config.MetricsConfig{}
		mc.Prometheus.Enabled = true
		mc.Prometheus.Port, _ = ct.NewOptIntGreaterThanZero(availablePort)
		manager, err := NewManager(mc, 0, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		defer manager.Close()
		assert.NotNil(t, manager.prometheusServer)
	})

	t.Run("listens on default port", func(t *testing.T) {
		mc := config.MetricsConfig{}
		mc.Prometheus.Enabled = true
		manager, err := NewManager(mc, 0, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		defer manager.Close()

		verifyPrometheusEndpointIsReachable(t, config.DefaultPrometheusPort, time.Second)
	})

	t.Run("listens on custom port", func(t *testing.T) {
		availablePort := st.GetAvailablePort(t)
		mc := config.MetricsConfig{}
		mc.Prometheus.Enabled = true
		mc.Prometheus.Port, _ = ct.NewOptIntGreaterThanZero(availablePort)
		manager, err := NewManager(mc, 0, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		defer manager.Close()

		verifyPrometheusEndpointIsReachable(t, availablePort, time.Second)
	})

	t.Run("returns error if port is unavailable", func(t *testing.T) {
		st.WithListenerForAnyPort(t, func(l net.Listener, usedPort int) {
			mc := config.MetricsConfig{}
			mc.Prometheus.Enabled = true
			mc.Prometheus.Port, _ = ct.NewOptIntGreaterThanZero(usedPort)
			_, err := NewManager(mc, 0, ldlog.NewDisabledLoggers())
			assert.Error(t, err)
		})
	})
}
