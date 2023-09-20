package relay

import (
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v7/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest/testclient"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEndpointsStatus(t *testing.T) {
	t.Run("basic properties", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvClientSide, st.EnvMobile)

		withStartedRelay(t, config, func(p relayTestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvMain.Config.SDKKey)),
				status, "environments", st.EnvMain.Name, "sdkKey")
			st.AssertJSONPathMatch(t, "connected", status, "environments", st.EnvMain.Name, "status")
			st.AssertJSONPathMatch(t, "VALID", status, "environments", st.EnvMain.Name, "connectionStatus", "state")

			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvClientSide.Config.SDKKey)),
				status, "environments", st.EnvClientSide.Name, "sdkKey")
			st.AssertJSONPathMatch(t, "507f1f77bcf86cd799439011",
				status, "environments", st.EnvClientSide.Name, "envId")
			st.AssertJSONPathMatch(t, "connected",
				status, "environments", st.EnvClientSide.Name, "status")

			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvMobile.Config.SDKKey)),
				status, "environments", st.EnvMobile.Name, "sdkKey")
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvMobile.Config.MobileKey)),
				status, "environments", st.EnvMobile.Name, "mobileKey")
			st.AssertJSONPathMatch(t, "connected",
				status, "environments", st.EnvMobile.Name, "status")

			st.AssertJSONPathMatch(t, "healthy", status, "status")
			st.AssertJSONPathMatch(t, p.relay.version, status, "version")
			st.AssertJSONPathMatch(t, ld.Version, status, "clientVersion")
		})
	})

	t.Run("connection interruption - less than DisconnectedStatusTime", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)
		config.Main.DisconnectedStatusTime = ct.NewOptDuration(time.Minute)

		withStartedRelay(t, config, func(p relayTestParams) {
			interruptedSinceTime := time.Now()

			envMain, inited := p.relay.getEnvironment(st.EnvMain.Config.SDKKey)
			require.NotNil(t, envMain)
			require.True(t, inited)
			clientMain := envMain.GetClient().(*testclient.FakeLDClient)
			clientMain.SetDataSourceStatus(interfaces.DataSourceStatus{
				State:      interfaces.DataSourceStateInterrupted,
				StateSince: interruptedSinceTime,
			})

			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			st.AssertJSONPathMatch(t, "connected", status, "environments", st.EnvMain.Name, "status")
			st.AssertJSONPathMatch(t, "INTERRUPTED", status, "environments", st.EnvMain.Name, "connectionStatus", "state")
			st.AssertJSONPathMatch(t, float64(ldtime.UnixMillisFromTime(interruptedSinceTime)), status,
				"environments", st.EnvMain.Name, "connectionStatus", "stateSince")

			st.AssertJSONPathMatch(t, "connected", status, "environments", st.EnvMobile.Name, "status")
			st.AssertJSONPathMatch(t, "VALID", status, "environments", st.EnvMobile.Name, "connectionStatus", "state")

			st.AssertJSONPathMatch(t, "healthy", status, "status")
		})
	})

	t.Run("connection interruption - greater than DisconnectedStatusTime", func(t *testing.T) {
		threshold := time.Millisecond * 10

		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)
		config.Main.DisconnectedStatusTime = ct.NewOptDuration(threshold)

		withStartedRelay(t, config, func(p relayTestParams) {
			interruptedSinceTime := time.Now()

			envMain, inited := p.relay.getEnvironment(st.EnvMain.Config.SDKKey)
			require.NotNil(t, envMain)
			require.True(t, inited)
			clientMain := envMain.GetClient().(*testclient.FakeLDClient)
			clientMain.SetDataSourceStatus(interfaces.DataSourceStatus{
				State:      interfaces.DataSourceStateInterrupted,
				StateSince: interruptedSinceTime,
			})

			time.Sleep(threshold + (time.Millisecond * 10))

			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			st.AssertJSONPathMatch(t, "disconnected", status, "environments", st.EnvMain.Name, "status")
			st.AssertJSONPathMatch(t, "INTERRUPTED", status, "environments", st.EnvMain.Name, "connectionStatus", "state")
			st.AssertJSONPathMatch(t, float64(ldtime.UnixMillisFromTime(interruptedSinceTime)), status,
				"environments", st.EnvMain.Name, "connectionStatus", "stateSince")

			st.AssertJSONPathMatch(t, "connected", status, "environments", st.EnvMobile.Name, "status")
			st.AssertJSONPathMatch(t, "VALID", status, "environments", st.EnvMobile.Name, "connectionStatus", "state")

			st.AssertJSONPathMatch(t, "degraded", status, "status")
		})
	})
}
