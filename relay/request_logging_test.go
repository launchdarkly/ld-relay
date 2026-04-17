package relay

import (
	"log/slog"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
)

func TestRequestLogging(t *testing.T) {
	url := "http://localhost/status" // must be a route that exists - not-found paths currently aren't logged

	t.Run("requests are not logged by default", func(t *testing.T) {
		config := c.Config{
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		withStartedRelayCustom(t, config, relayTestBehavior{doNotEnableDebugLogging: true}, func(p relayTestParams) {
			req, _ := http.NewRequest("GET", url, nil)
			_, _ = st.DoRequest(req, p.relay)
			assert.False(t, p.mockHandler.HasMessage(slog.LevelDebug, "request completed"))
		})
	})

	t.Run("requests are logged when debug logging is enabled", func(t *testing.T) {
		config := c.Config{
			Main:        c.MainConfig{LogLevel: c.NewOptLogLevel(slog.LevelDebug)},
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		withStartedRelay(t, config, func(p relayTestParams) {
			req, _ := http.NewRequest("GET", url, nil)
			_, _ = st.DoRequest(req, p.relay)
			assert.True(t, p.mockHandler.HasMessage(slog.LevelDebug, "request completed"))
		})
	})
}
