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
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
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

// TestEndpointsStatusDuringInFlightRotation drives the real /status handler while an SDK-key re-anchor
// is in flight: the new anchor's client build is wedged via a gated client factory, so the rotation has
// been reconciled but not yet committed. The status request must complete with 200, report the
// PRE-rotation anchor (the rotator has not flipped its anchor pointer until the build is committed), and
// expose a self-consistent accepted set (the reported anchor is present in sdkKeys[]). Once the build is
// released and the rotation commits, /status reports the new anchor. Run under -race to catch any
// unsynchronized read between the status handler and the concurrent re-anchor.
func TestEndpointsStatusDuringInFlightRotation(t *testing.T) {
	const newAnchor = c.SDKKey("sdk-status-rotation-new-anchor")

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	// Gated factory: healthy for every key except newAnchor, whose build wedges until released. This
	// holds the re-anchor mid-flight (pre-commit) so /status observes the pre-rotation anchor.
	buildEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	inner := testclient.FakeLDClientFactory(true)
	gated := func(sdkKey c.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == newAnchor {
			buildEntered <- struct{}{}
			<-release
		}
		return inner(sdkKey, cfg, timeout)
	}

	relay, err := newRelayInternal(config, relayInternalOptions{
		loggers:       mockLog.Loggers,
		clientFactory: gated,
	})
	require.NoError(t, err)
	defer relay.Close()
	require.NoError(t, relay.waitForAllClients(time.Second))

	env, err := relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
	require.NoError(t, err)
	require.NotNil(t, env)

	anchor := st.EnvMain.Config.SDKKey
	graceExpiry := time.Now().Add(time.Hour)
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: newAnchor}).
		WithSDKKey(credential.SDKKeyParams{Value: anchor, Expiry: util.PtrOrNil(graceExpiry)}).
		Build()
	require.NoError(t, err)

	// Drive the re-anchor on a background goroutine; it blocks in the gated build, holding the rotation
	// mid-flight (pre-commit).
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		env.ReconcileCredentials(set)
	}()
	<-buildEntered // the new anchor's build is wedged: the rotation is in flight, not yet committed.

	// /status must complete and report the pre-rotation anchor while the rotation is in flight.
	r, _ := http.NewRequest("GET", "http://localhost/status", nil)
	result, body := st.DoRequest(r, relay)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	envStatus := ldvalue.Parse(body).GetByKey("environments").GetByKey(st.EnvMain.Name)

	// The scalar anchor is still the pre-rotation key (the anchor pointer flips only on commit).
	assert.Equal(t, sdks.ObscureKey(string(anchor)), envStatus.GetByKey("sdkKey").StringValue(),
		"status reports the pre-rotation anchor while the rotation is mid-flight")

	// The arrays are self-consistent: the reported anchor is present in sdkKeys[].
	sdkKeys := envStatus.GetByKey("sdkKeys")
	require.False(t, findKeyStatusByValue(sdkKeys, sdks.ObscureKey(string(anchor))).IsNull(),
		"the reported anchor must be present in sdkKeys[]")

	// The env still serves the previous anchor's client, so it reports connected.
	assert.Equal(t, "connected", envStatus.GetByKey("status").StringValue())

	// Release the build; the rotation commits.
	close(release)
	<-reconcileDone

	// After the commit, /status reports the new anchor, still present in a consistent sdkKeys[].
	r2, _ := http.NewRequest("GET", "http://localhost/status", nil)
	result2, body2 := st.DoRequest(r2, relay)
	assert.Equal(t, http.StatusOK, result2.StatusCode)
	envStatus2 := ldvalue.Parse(body2).GetByKey("environments").GetByKey(st.EnvMain.Name)
	assert.Equal(t, sdks.ObscureKey(string(newAnchor)), envStatus2.GetByKey("sdkKey").StringValue(),
		"after the commit, status reports the new anchor")
	require.False(t, findKeyStatusByValue(envStatus2.GetByKey("sdkKeys"), sdks.ObscureKey(string(newAnchor))).IsNull(),
		"the new anchor must be present in sdkKeys[] after the commit")
}
