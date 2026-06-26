package relay

import (
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

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

			envMain, err := p.relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))

			require.NotNil(t, envMain)
			require.Nil(t, err)
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

			envMain, err := p.relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
			require.NotNil(t, envMain)
			require.Nil(t, err)
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

// TestBuildKeyStatusSlice verifies the sdkKeys[] helper that converts rotator entries to the JSON
// representation used by the status endpoint.
func TestBuildKeyStatusSlice(t *testing.T) {
	t.Run("empty entries yields empty slice", func(t *testing.T) {
		result := buildKeyStatusSlice(nil)
		assert.Empty(t, result)
	})

	t.Run("permanent key has no expiry field", func(t *testing.T) {
		entries := []credential.SDKKeyEntry{
			{Value: c.SDKKey("sdk-abc123"), Identifier: "default", Expiry: nil},
		}
		result := buildKeyStatusSlice(entries)
		require.Len(t, result, 1)
		assert.Equal(t, "default", result[0].Key)
		assert.Equal(t, sdks.ObscureKey("sdk-abc123"), result[0].Value)
		assert.Nil(t, result[0].Expiry)
	})

	t.Run("expiring key has expiry in Unix milliseconds", func(t *testing.T) {
		expiry := time.Date(2099, 6, 1, 12, 0, 0, 0, time.UTC)
		entries := []credential.SDKKeyEntry{
			{Value: c.SDKKey("sdk-old"), Identifier: "old-key", Expiry: &expiry},
		}
		result := buildKeyStatusSlice(entries)
		require.Len(t, result, 1)
		require.NotNil(t, result[0].Expiry)
		assert.Equal(t, expiry.UnixMilli(), *result[0].Expiry)
	})

	t.Run("preserves entry order from input", func(t *testing.T) {
		entries := []credential.SDKKeyEntry{
			{Value: c.SDKKey("sdk-a"), Identifier: "a"},
			{Value: c.SDKKey("sdk-b"), Identifier: "b"},
		}
		result := buildKeyStatusSlice(entries)
		require.Len(t, result, 2)
		assert.Equal(t, "a", result[0].Key)
		assert.Equal(t, "b", result[1].Key)
	})
}

// TestBuildMobileKeyStatusSlice verifies the mobileKeys[] helper mirrors buildKeyStatusSlice behaviour.
func TestBuildMobileKeyStatusSlice(t *testing.T) {
	t.Run("empty entries yields empty slice", func(t *testing.T) {
		result := buildMobileKeyStatusSlice(nil)
		assert.Empty(t, result)
	})

	t.Run("mobile key value is obscured", func(t *testing.T) {
		entries := []credential.MobileKeyEntry{
			{Value: c.MobileKey("mob-secret"), Identifier: "mob-1", Expiry: nil},
		}
		result := buildMobileKeyStatusSlice(entries)
		require.Len(t, result, 1)
		assert.Equal(t, "mob-1", result[0].Key)
		assert.Equal(t, sdks.ObscureKey("mob-secret"), result[0].Value)
		assert.Nil(t, result[0].Expiry)
	})

	t.Run("expiring mobile key has expiry in Unix milliseconds", func(t *testing.T) {
		expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		entries := []credential.MobileKeyEntry{
			{Value: c.MobileKey("mob-old"), Identifier: "mob-old", Expiry: &expiry},
		}
		result := buildMobileKeyStatusSlice(entries)
		require.Len(t, result, 1)
		require.NotNil(t, result[0].Expiry)
		assert.Equal(t, expiry.UnixMilli(), *result[0].Expiry)
	})
}
