package relay

import (
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v7/config"
	st "github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
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

			p.mockLog.AssertMessageMatch(t, false, ldlog.Debug, "method=GET url="+url)
		})
	})

	t.Run("requests are logged when debug logging is enabled", func(t *testing.T) {
		config := c.Config{
			Main:        c.MainConfig{LogLevel: c.NewOptLogLevel(ldlog.Debug)},
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		withStartedRelay(t, config, func(p relayTestParams) {
			req, _ := http.NewRequest("GET", url, nil)
			_, _ = st.DoRequest(req, p.relay)

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "method=GET url="+url)
		})
	})
}
