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
	"github.com/launchdarkly/ld-relay/v8/internal/util"

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

		t.Run("sdkKeys/mobileKeys arrays carry the full accepted set including the anchor", func(t *testing.T) {
			var config c.Config
			config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)

			withStartedRelay(t, config, func(p relayTestParams) {
				r, _ := http.NewRequest("GET", "http://localhost/status", nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)
				status := ldvalue.Parse(body)

				// EnvMain is a manually-configured single-key env: sdkKeys[] is present and contains
				// exactly the anchor (full set includes it); manual config carries no key identifier.
				sdkKeys := status.GetByKey("environments").GetByKey(st.EnvMain.Name).GetByKey("sdkKeys")
				require.Equal(t, 1, sdkKeys.Count(), "sdkKeys must contain the anchor")
				assert.Equal(t, sdks.ObscureKey(string(st.EnvMain.Config.SDKKey)),
					sdkKeys.GetByIndex(0).GetByKey("value").StringValue())

				// EnvMain has no mobile key — mobileKeys is present but empty.
				mobileKeys := status.GetByKey("environments").GetByKey(st.EnvMain.Name).GetByKey("mobileKeys")
				assert.Equal(t, 0, mobileKeys.Count())
				assert.Equal(t, ldvalue.ArrayType, mobileKeys.Type(), "mobileKeys present (not null) even when empty")

				// EnvMobile has both: its mobile key appears in mobileKeys[].
				mobMobileKeys := status.GetByKey("environments").GetByKey(st.EnvMobile.Name).GetByKey("mobileKeys")
				require.Equal(t, 1, mobMobileKeys.Count())
				assert.Equal(t, sdks.ObscureKey(string(st.EnvMobile.Config.MobileKey)),
					mobMobileKeys.GetByIndex(0).GetByKey("value").StringValue())
			})
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

// findKeyStatusByValue returns the sdkKeys[]/mobileKeys[] entry whose obscured "value" matches, or a
// null value. Array entry order is unspecified, so callers look entries up by value.
func findKeyStatusByValue(arr ldvalue.Value, obscuredValue string) ldvalue.Value {
	for i := 0; i < arr.Count(); i++ {
		if arr.GetByIndex(i).GetByKey("value").StringValue() == obscuredValue {
			return arr.GetByIndex(i)
		}
	}
	return ldvalue.Null()
}

// TestEndpointsStatusExpiringSDKKey drives a multi-key environment through the real /status handler:
// it reconciles an env to an anchor plus two non-anchor expiring SDK keys and asserts the
// expiringSdkKey selection, the per-key expiry/identifier serialization in sdkKeys[], and that the
// soonest-expiry pick is deterministic on an exact expiry tie.
func TestEndpointsStatusExpiringSDKKey(t *testing.T) {
	getStatus := func(t *testing.T, p relayTestParams, set credential.AcceptedSet) ldvalue.Value {
		env, err := p.relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
		require.NoError(t, err)
		require.NotNil(t, env)
		env.ReconcileCredentials(set)

		r, _ := http.NewRequest("GET", "http://localhost/status", nil)
		result, body := st.DoRequest(r, p.relay)
		require.Equal(t, http.StatusOK, result.StatusCode)
		return ldvalue.Parse(body).GetByKey("environments").GetByKey(st.EnvMain.Name)
	}

	t.Run("soonest-expiring non-anchor key, with expiry and identifier surfaced", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain)
		withStartedRelay(t, config, func(p relayTestParams) {
			anchor := st.EnvMain.Config.SDKKey
			soon := time.Now().Add(1 * time.Hour)
			later := time.Now().Add(2 * time.Hour)
			set, err := credential.NewAcceptedSetBuilder().
				WithAnchor(credential.SDKKeyParams{Value: anchor}).
				WithSDKKey(credential.SDKKeyParams{Value: "sdk-soon", Key: util.PtrOrNil("soon-key"), Expiry: util.PtrOrNil(soon)}).
				WithSDKKey(credential.SDKKeyParams{Value: "sdk-later", Expiry: util.PtrOrNil(later)}).
				Build()
			require.NoError(t, err)

			envStatus := getStatus(t, p, set)

			// expiringSdkKey is the obscured soonest-expiring non-anchor key.
			st.AssertJSONPathMatch(t, sdks.ObscureKey("sdk-soon"), envStatus, "expiringSdkKey")

			// sdkKeys[] carries the full set (anchor + both non-anchor keys).
			sdkKeys := envStatus.GetByKey("sdkKeys")
			require.Equal(t, 3, sdkKeys.Count())

			// The expiring key surfaces its identifier and Unix-millis expiry.
			soonEntry := findKeyStatusByValue(sdkKeys, sdks.ObscureKey("sdk-soon"))
			require.False(t, soonEntry.IsNull())
			assert.Equal(t, "soon-key", soonEntry.GetByKey("key").StringValue())
			assert.Equal(t, float64(soon.UnixMilli()), soonEntry.GetByKey("expiry").Float64Value())

			// A key with no identifier omits "key" entirely (omitempty, nil pointer).
			laterEntry := findKeyStatusByValue(sdkKeys, sdks.ObscureKey("sdk-later"))
			require.False(t, laterEntry.IsNull())
			assert.True(t, laterEntry.GetByKey("key").IsNull(), `"key" must be omitted when the source carried no identifier`)
		})
	})

	t.Run("tie on expiry resolves deterministically to the smaller value", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain)
		withStartedRelay(t, config, func(p relayTestParams) {
			anchor := st.EnvMain.Config.SDKKey
			sameExpiry := time.Now().Add(1 * time.Hour)
			set, err := credential.NewAcceptedSetBuilder().
				WithAnchor(credential.SDKKeyParams{Value: anchor}).
				WithSDKKey(credential.SDKKeyParams{Value: "sdk-bbb", Expiry: util.PtrOrNil(sameExpiry)}).
				WithSDKKey(credential.SDKKeyParams{Value: "sdk-aaa", Expiry: util.PtrOrNil(sameExpiry)}).
				Build()
			require.NoError(t, err)

			envStatus := getStatus(t, p, set)
			// With equal expiries, the smaller value (sdk-aaa) wins deterministically.
			st.AssertJSONPathMatch(t, sdks.ObscureKey("sdk-aaa"), envStatus, "expiringSdkKey")
		})
	})
}

// TestKeyStatus verifies the helper that converts an accepted key into its status-endpoint JSON form.
func TestKeyStatus(t *testing.T) {
	strptr := func(s string) *string { return &s }

	t.Run("permanent key with identifier", func(t *testing.T) {
		ks := keyStatus(credential.AcceptedKey{
			Type: credential.KeyTypeServer, Value: "sdk-abc123", Key: strptr("default"),
		})
		assert.Equal(t, "default", ks.Key)
		assert.Equal(t, sdks.ObscureKey("sdk-abc123"), ks.Value)
		assert.Nil(t, ks.Expiry)
	})

	t.Run("nil identifier yields empty Key (omitted in JSON)", func(t *testing.T) {
		ks := keyStatus(credential.AcceptedKey{
			Type: credential.KeyTypeServer, Value: "sdk-legacy", Key: nil,
		})
		assert.Equal(t, "", ks.Key)
	})

	t.Run("expiring key has expiry in Unix milliseconds", func(t *testing.T) {
		expiry := time.Date(2099, 6, 1, 12, 0, 0, 0, time.UTC)
		ks := keyStatus(credential.AcceptedKey{
			Type: credential.KeyTypeServer, Value: "sdk-old", Key: strptr("old-key"), Expiry: &expiry,
		})
		require.NotNil(t, ks.Expiry)
		assert.Equal(t, expiry.UnixMilli(), *ks.Expiry)
	})

	t.Run("mobile key value is obscured", func(t *testing.T) {
		ks := keyStatus(credential.AcceptedKey{
			Type: credential.KeyTypeMobile, Value: "mob-secret", Key: strptr("mob-1"),
		})
		assert.Equal(t, sdks.ObscureKey("mob-secret"), ks.Value)
	})
}
