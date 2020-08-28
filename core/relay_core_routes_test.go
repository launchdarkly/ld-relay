package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
)

func TestRequestLogging(t *testing.T) {
	url := "http://localhost/status" // must be a route that exists - not-found paths currently aren't logged

	t.Run("requests are not logged by default", func(t *testing.T) {
		config := c.Config{
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		mockLog := ldlogtest.NewMockLog()
		core, err := NewRelayCore(config, mockLog.Loggers, testclient.FakeLDClientFactory(true), "", "", false)
		require.NoError(t, err)
		defer core.Close()

		handler := core.MakeRouter()
		req, _ := http.NewRequest("GET", url, nil)
		_, _ = st.DoRequest(req, handler)

		mockLog.AssertMessageMatch(t, false, ldlog.Debug, "method=GET url="+url)
	})

	t.Run("requests are logged when debug logging is enabled", func(t *testing.T) {
		config := c.Config{
			Main:        c.MainConfig{LogLevel: c.NewOptLogLevel(ldlog.Debug)},
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		mockLog := ldlogtest.NewMockLog()
		core, err := NewRelayCore(config, mockLog.Loggers, testclient.FakeLDClientFactory(true), "", "", false)
		require.NoError(t, err)
		defer core.Close()

		handler := core.MakeRouter()
		req, _ := http.NewRequest("GET", url, nil)
		_, _ = st.DoRequest(req, handler)

		mockLog.AssertMessageMatch(t, true, ldlog.Debug, "method=GET url="+url)
	})
}
