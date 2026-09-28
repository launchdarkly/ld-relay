package relay

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/api"
	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testEnvBasic = st.TestEnv{
	Name: "ignore this configured name",
	Config: c.EnvConfig{
		SDKKey:    c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-2e1e4d8e52e9"),
		MobileKey: c.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42ec"),
		EnvID:     c.EnvironmentID("507f1f77bcf86cd79943902a"),
	},
	EnvKey:   "production",
	EnvName:  "Production",
	ProjKey:  "my-application",
	ProjName: "My Application",
}

var testEnvWithExpiringKey = st.TestEnv{
	Name: "ignore this configured name",
	Config: c.EnvConfig{
		SDKKey:    c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-2e1e4d8e52ea"),
		MobileKey: c.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42ed"),
		EnvID:     c.EnvironmentID("507f1f77bcf86cd79943902b"),
	},
	EnvKey:             "production-with-expiring-key",
	EnvName:            "Production with Expiring Key",
	ProjKey:            "my-application",
	ProjName:           "My Application",
	ExpiringSDKKey:     c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-000001111123"),
	ExpiringSDKKeyTime: ldtime.UnixMillisecondTime(100000),
}

var autoConfigTestEnvs = map[c.EnvironmentID]st.TestEnv{
	testEnvBasic.Config.EnvID:           testEnvBasic,
	testEnvWithExpiringKey.Config.EnvID: testEnvWithExpiringKey,
}

// Unlike relay_endpoints_test.go, which runs with a local configuration, here we are testing
// endpoint responses for a Relay instance that is auto-configured. We don't run the full test
// suite this way, since most things behave the same with or without auto-config once the
// environment list has been obtained; we just want to make sure it starts up correctly in
// general and test for any specific responses that should be different.

func withStartedAutoConfigRelay(t *testing.T, configWithEnvs c.Config, action func(relayTestParams)) {
	autoConfigEvent := transformEnvConfigsToAutoConfig(configWithEnvs)
	autoConfigHandler, autoConfigStream := httphelpers.SSEHandler(&autoConfigEvent)
	defer autoConfigStream.Close()

	server := httptest.NewServer(autoConfigHandler)
	defer server.Close()

	fullConfig := configWithEnvs
	fullConfig.AutoConfig.Key = testAutoConfKey
	fullConfig.Environment = nil
	fullConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	withStartedRelayCustom(t, fullConfig, relayTestBehavior{skipWaitForEnvironments: true}, func(p relayTestParams) {
		waitForAutoConfigInit(t, p.relay, configWithEnvs)
		action(p)
	})
}

// withAutoConfigRelayAndStream is withStartedAutoConfigRelay with the stream handed to the action,
// so a test can deliver further events after start-up. The plain version keeps the stream private
// because most tests only need the initial configuration.
func withAutoConfigRelayAndStream(
	t *testing.T,
	configWithEnvs c.Config,
	action func(relayTestParams, httphelpers.SSEStreamControl),
) {
	autoConfigEvent := transformEnvConfigsToAutoConfig(configWithEnvs)
	autoConfigHandler, autoConfigStream := httphelpers.SSEHandler(&autoConfigEvent)
	defer autoConfigStream.Close()

	server := httptest.NewServer(autoConfigHandler)
	defer server.Close()

	fullConfig := configWithEnvs
	fullConfig.AutoConfig.Key = testAutoConfKey
	fullConfig.Environment = nil
	fullConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	withStartedRelayCustom(t, fullConfig, relayTestBehavior{skipWaitForEnvironments: true}, func(p relayTestParams) {
		waitForAutoConfigInit(t, p.relay, configWithEnvs)
		action(p, autoConfigStream)
	})
}

// makeRefusedPatch builds a patch for env whose sdkKeys array omits the designated key, which
// BuildAcceptedSet refuses. The JSON is well formed, so this exercises the credential check rather
// than the parse.
func makeRefusedPatch(env st.TestEnv, version int) httphelpers.SSEEvent {
	rep := envfactory.EnvironmentRep{
		EnvID:    env.Config.EnvID,
		EnvKey:   env.EnvKey,
		EnvName:  env.EnvName,
		ProjKey:  env.ProjKey,
		ProjName: env.ProjName,
		MobKey:   env.Config.MobileKey,
		SDKKey:   envfactory.SDKKeyRep{Value: env.Config.SDKKey},
		SDKKeys: []envfactory.ConcurrentKeyRep{
			{Key: "some-other-key", Value: "a-key-that-is-not-the-designated-one"},
		},
		Version: version,
	}
	data, _ := json.Marshal(autoconfig.PatchMessageData{
		Path: "/environments/" + string(env.Config.EnvID),
		Data: mustMarshalJSON(rep),
	})
	return httphelpers.SSEEvent{Event: autoconfig.PatchEvent, Data: string(data)}
}

func mustMarshalJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func TestAutoConfigRefusedPatchLeavesTheEnvironmentServingItsPreviousCredentials(t *testing.T) {
	// The stream manager validates a credential payload before the message receiver records its
	// version, so a refused patch never reaches the relay at all. This asserts the consequence that
	// matters to an operator, which no test covered end to end: the environment stays in the
	// document and its existing credentials keep working, rather than being torn down or replaced
	// by the unusable ones the patch carried.
	basic := testEnvBasic.Config
	config := c.Config{Environment: map[string]*c.EnvConfig{"basic": &basic}}

	withAutoConfigRelayAndStream(t, config, func(p relayTestParams, stream httphelpers.SSEStreamControl) {
		_, err := p.relay.getEnvironment(testEnvBasic.Config.SDKKey)
		require.NoError(t, err, "the environment must be serving before the refused patch")

		stream.Enqueue(makeRefusedPatch(testEnvBasic, 1))

		// The refusal is reported on the auto-config status, which is the signal an operator has.
		require.Eventually(t, func() bool {
			document := fetchStatusDocument(t, p.relay)
			return document.GetByKey("autoConfigStatus").GetByKey("lastError").
				GetByKey("kind").StringValue() == "INVALID_DATA"
		}, 2*time.Second, 20*time.Millisecond, "the refusal must be reported on autoConfigStatus")

		// The environment is untouched: still served, still on the credentials it had.
		_, err = p.relay.getEnvironment(testEnvBasic.Config.SDKKey)
		assert.NoError(t, err, "a refused patch must not revoke the credentials that were working")
		_, err = p.relay.getEnvironment(c.SDKKey("a-key-that-is-not-the-designated-one"))
		assert.Error(t, err, "a key from a refused patch must not authenticate")
	})
}

func transformEnvConfigsToAutoConfig(config c.Config) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{Path: "/", Data: autoconfig.PutContent{
		Environments: make(map[c.EnvironmentID]envfactory.EnvironmentRep),
	}}
	for _, envConfig := range config.Environment {
		env, ok := autoConfigTestEnvs[envConfig.EnvID]
		if !ok {
			panic("can't run auto-config with an environment that's not in autoConfigTestEnvs")
		}
		rep := envfactory.EnvironmentRep{
			EnvID:    env.Config.EnvID,
			EnvKey:   env.EnvKey,
			EnvName:  env.EnvName,
			ProjKey:  env.ProjKey,
			ProjName: env.ProjName,
			MobKey:   env.Config.MobileKey,
			SDKKey: envfactory.SDKKeyRep{
				Value: env.Config.SDKKey,
			},
		}
		if env.ExpiringSDKKey.Defined() {
			rep.SDKKey.Expiring.Value = env.ExpiringSDKKey
			rep.SDKKey.Expiring.Timestamp = ldtime.UnixMillisNow() + env.ExpiringSDKKeyTime
		}
		data.Data.Environments[env.Config.EnvID] = rep
	}
	jsonData, _ := json.Marshal(data)
	return httphelpers.SSEEvent{Event: autoconfig.PutEvent, Data: string(jsonData)}
}

func waitForAutoConfigInit(t *testing.T, r *Relay, configWithEnvs c.Config) {
	// Auto-config initialization is done in the background, so we need to wait until it has happened before
	// we run the tests
	expectedEnvCount := 0
	for _, ec := range configWithEnvs.Environment {
		if ec.EnvID != "" {
			expectedEnvCount++
		}
	}
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond * 10)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			envs := r.getAllEnvironments()
			if len(envs) == expectedEnvCount {
				return
			}
		case <-deadline:
			require.Fail(t, "timed out waiting for auto-configuration to happen")
		}
	}
}

func TestAutoConfigStatusEndpoints(t *testing.T) {
	t.Run("basic status properties", func(t *testing.T) {
		envConfig := testEnvBasic
		config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
		withStartedAutoConfigRelay(t, config, func(p relayTestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			envKey := string(envConfig.Config.EnvID)

			st.AssertJSONPathMatch(t, envKey,
				status, "environments", envKey, "envId")
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(envConfig.Config.SDKKey)),
				status, "environments", envKey, "sdkKey")
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(envConfig.Config.MobileKey)),
				status, "environments", envKey, "mobileKey")
			st.AssertJSONPathMatch(t, envConfig.EnvKey,
				status, "environments", envKey, "envKey")
			st.AssertJSONPathMatch(t, envConfig.EnvName,
				status, "environments", envKey, "envName")
			st.AssertJSONPathMatch(t, envConfig.ProjKey,
				status, "environments", envKey, "projKey")
			st.AssertJSONPathMatch(t, envConfig.ProjName,
				status, "environments", envKey, "projName")
			st.AssertJSONPathMatch(t, "connected",
				status, "environments", envKey, "status")

			st.AssertJSONPathMatch(t, "healthy", status, "status")
			st.AssertJSONPathMatch(t, p.relay.version, status, "version")
			st.AssertJSONPathMatch(t, ld.Version, status, "clientVersion")
		})
	})

	t.Run("auto-config stream status", func(t *testing.T) {
		envConfig := testEnvBasic
		config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
		withStartedAutoConfigRelay(t, config, func(p relayTestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			st.AssertJSONPathMatch(t, "VALID", status, "autoConfigStatus", "state")
			assert.False(t, status.GetByKey("autoConfigStatus").GetByKey("stateSince").IsNull())
			assert.True(t, status.GetByKey("autoConfigStatus").GetByKey("lastError").IsNull(),
				"a working stream should report no error")

			// The state is assertable with an "expect" clause, which is the point of reporting it.
			r, _ = http.NewRequest("GET", "http://localhost/status?expect=autoConfigStatus.state%3DVALID", nil)
			result, _ = st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
		})
	})

	t.Run("expiring SDK key", func(t *testing.T) {
		envConfig := testEnvWithExpiringKey
		config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
		withStartedAutoConfigRelay(t, config, func(p relayTestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			envKey := string(envConfig.Config.EnvID)

			st.AssertJSONPathMatch(t, envKey,
				status, "environments", envKey, "envId")
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(envConfig.Config.SDKKey)),
				status, "environments", envKey, "sdkKey")
			// Per-key expiry lives in the sdkKeys array now. The anchor and the expiring key are both
			// accepted, so the array carries two entries and only one of them has an expiry.
			sdkKeys := status.GetByKey("environments").GetByKey(envKey).GetByKey("sdkKeys")
			require.Equal(t, 2, sdkKeys.Count(), "the anchor and the expiring key are both accepted")
			expiringValue := sdks.ObscureKey(string(envConfig.ExpiringSDKKey))
			var foundExpiring bool
			for i := 0; i < sdkKeys.Count(); i++ {
				entry := sdkKeys.GetByIndex(i)
				if entry.GetByKey("value").StringValue() != expiringValue {
					continue
				}
				foundExpiring = true
				assert.False(t, entry.GetByKey("expiry").IsNull(), "the expiring key carries its expiry")
			}
			assert.True(t, foundExpiring, "the expiring key appears in sdkKeys")
		})
	})
}

func TestRelayReturns503ForAllEnvironmentsUntilAutoConfigIsComplete(t *testing.T) {
	envConfig := testEnvBasic
	config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
	autoConfigEvent := transformEnvConfigsToAutoConfig(config)
	autoConfigHandler, autoConfigStream := httphelpers.SSEHandler(&autoConfigEvent)

	handlerHasReceivedRequestCh := make(chan struct{}, 1)
	allowHandlerToRespondCh := make(chan struct{}, 1)
	handlerThatWaitsForGate := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlerHasReceivedRequestCh <- struct{}{}
		<-allowHandlerToRespondCh
		autoConfigHandler.ServeHTTP(w, req)
	})
	server := httptest.NewServer(handlerThatWaitsForGate)
	defer server.Close()
	defer autoConfigStream.Close()

	entConfig := config
	entConfig.AutoConfig.Key = testAutoConfKey
	entConfig.Environment = nil
	entConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	r, err := newRelayInternal(entConfig, relayInternalOptions{
		logger:        slog.Default(),
		clientFactory: testclient.CreateDummyClient,
	})
	require.NoError(t, err)
	defer r.Close()

	<-handlerHasReceivedRequestCh

	pollUrl := "http://fake/sdk/evalx/users/eyJrZXkiOiJmb28ifQ"
	req, _ := http.NewRequest("GET", pollUrl, nil)
	req.Header.Add("Authorization", string(envConfig.Config.SDKKey))

	rr1 := httptest.NewRecorder()
	r.Handler.ServeHTTP(rr1, req)
	require.Equal(t, 503, rr1.Result().StatusCode)

	allowHandlerToRespondCh <- struct{}{}

	require.Eventually(t, func() bool {
		rr2 := httptest.NewRecorder()
		r.Handler.ServeHTTP(rr2, req)
		if rr2.Result().StatusCode == 200 {
			return true
		}
		require.Equal(t, 503, rr2.Result().StatusCode)
		return false
	}, time.Second, time.Millisecond*50, "Relay kept returning 503 after receiving configuration")
}

// When the relay is not yet fully configured, a per-environment status request with an "expect"
// clause must return the route-level 503 before any clause is evaluated -- not 412 or 400.
func TestStatusExpectReturns503BeforeEvaluationWhenNotReady(t *testing.T) {
	envConfig := testEnvBasic
	config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
	autoConfigEvent := transformEnvConfigsToAutoConfig(config)
	autoConfigHandler, autoConfigStream := httphelpers.SSEHandler(&autoConfigEvent)

	handlerHasReceivedRequestCh := make(chan struct{}, 1)
	allowHandlerToRespondCh := make(chan struct{}, 1)
	handlerThatWaitsForGate := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlerHasReceivedRequestCh <- struct{}{}
		<-allowHandlerToRespondCh
		autoConfigHandler.ServeHTTP(w, req)
	})
	server := httptest.NewServer(handlerThatWaitsForGate)
	defer server.Close()
	defer autoConfigStream.Close()

	entConfig := config
	entConfig.AutoConfig.Key = testAutoConfKey
	entConfig.Environment = nil
	entConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	r, err := newRelayInternal(entConfig, relayInternalOptions{
		logger:        slog.Default(),
		clientFactory: testclient.CreateDummyClient,
	})
	require.NoError(t, err)
	defer r.Close()

	<-handlerHasReceivedRequestCh

	// Even a clause that would fail evaluation must not be reached: the not-ready 503 wins.
	req, _ := http.NewRequest("GET",
		"http://fake/status/anything?expect=status=connected", nil)
	rr := httptest.NewRecorder()
	r.Handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Result().StatusCode)

	allowHandlerToRespondCh <- struct{}{}
}

// Once the auto-config stream breaks, the status document must say so. Relay keeps serving the
// environments it already knows about, so nothing else in the document changes.
func TestAutoConfigStatusReportsAnInterruptedStream(t *testing.T) {
	envConfig := testEnvBasic
	config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
	autoConfigEvent := transformEnvConfigsToAutoConfig(config)
	autoConfigHandler, autoConfigStream := httphelpers.SSEHandler(&autoConfigEvent)
	defer autoConfigStream.Close()

	// The first connection works, so the stream reaches VALID. Every connection after it fails, so
	// the interrupted state persists instead of flickering back.
	handler := httphelpers.SequentialHandler(
		autoConfigHandler,
		httphelpers.HandlerWithStatus(503),
		httphelpers.HandlerWithStatus(503),
		httphelpers.HandlerWithStatus(503),
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	entConfig := config
	entConfig.AutoConfig.Key = testAutoConfKey
	entConfig.Environment = nil
	entConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	r, err := newRelayInternal(entConfig, relayInternalOptions{
		logger:        slog.Default(),
		clientFactory: testclient.CreateDummyClient,
	})
	require.NoError(t, err)
	defer r.Close()

	waitForAutoConfigInit(t, r, config)

	autoConfigStream.EndAll() // drop the connection, so the retries meet the failing handler

	readStatus := func() ldvalue.Value {
		req, _ := http.NewRequest("GET", "http://localhost/status", nil)
		_, body := st.DoRequest(req, r)
		return ldvalue.Parse(body)
	}

	// The dropped connection reports a network error first, and the failing handler that the retry
	// meets reports the HTTP error, so this waits for the state the stream settles in.
	var status ldvalue.Value
	require.Eventuallyf(t, func() bool {
		status = readStatus()
		autoConfig := status.GetByKey("autoConfigStatus")
		return autoConfig.GetByKey("state").StringValue() == "INTERRUPTED" &&
			autoConfig.GetByKey("lastError").GetByKey("kind").StringValue() == "ERROR_RESPONSE"
	}, 2*time.Second, 10*time.Millisecond,
		"auto-config state never reported an interrupting HTTP error: %s", status.String())

	st.AssertJSONPathMatch(t, "ERROR_RESPONSE", status, "autoConfigStatus", "lastError", "kind")
	st.AssertJSONPathMatch(t, float64(503), status, "autoConfigStatus", "lastError", "statusCode")

	// The environment is still served, and the relay-level status is unchanged: a broken
	// auto-config stream does not stop flag serving, so probes that only check "status" keep
	// passing. That is why the auto-config state is reported separately.
	envKey := string(envConfig.Config.EnvID)
	st.AssertJSONPathMatch(t, "connected", status, "environments", envKey, "status")
	st.AssertJSONPathMatch(t, "healthy", status, "status")
}

func TestStatusListsRefusedEnvironments(t *testing.T) {
	// A refused environment is absent from `environments`, and both the top-level status and
	// autoConfigStatus stay healthy, because the connection works and the rest of the configuration
	// applied. This list is the only thing in the document that says an environment Relay was told
	// about is not being served.
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		actions := &relayAutoConfigActions{r: p.relay}

		actions.SetRefusedEnvironments(map[c.EnvironmentID]string{
			"env-zulu":  "malformed credential payload",
			"env-alpha": "malformed credential payload",
		})

		document := fetchStatusDocument(t, p.relay)
		refused := document.GetByKey("refusedEnvironments")
		require.Equal(t, 2, refused.Count())
		// Sorted by environment ID, so a monitor comparing documents between requests sees a stable
		// order rather than Go's map iteration.
		assert.Equal(t, "env-alpha", refused.GetByIndex(0).GetByKey("envId").StringValue())
		assert.Equal(t, "env-zulu", refused.GetByIndex(1).GetByKey("envId").StringValue())
		assert.Equal(t, "malformed credential payload",
			refused.GetByIndex(0).GetByKey("reason").StringValue())
		assert.False(t, refused.GetByIndex(0).GetByKey("serving").BoolValue(),
			"an environment Relay has no configuration for is not being served")

		assert.Equal(t, api.StatusHealthy, document.GetByKey("status").StringValue(),
			"a refused environment must not make the whole relay report degraded")

		// A put is the whole environment set, so its refusals replace the previous ones rather than
		// adding to them. Without that, an environment refused once would stay listed after a later
		// put stopped mentioning it at all.
		actions.SetRefusedEnvironments(map[c.EnvironmentID]string{
			"env-zulu": "malformed credential payload",
		})

		document = fetchStatusDocument(t, p.relay)
		refused = document.GetByKey("refusedEnvironments")
		require.Equal(t, 1, refused.Count(),
			"a put that no longer refuses an environment must retire its entry")
		assert.Equal(t, "env-zulu", refused.GetByIndex(0).GetByKey("envId").StringValue())

		// A patch reports one environment, so it adds to the set rather than replacing it.
		actions.EnvironmentRefused("env-bravo", "malformed credential payload")

		document = fetchStatusDocument(t, p.relay)
		require.Equal(t, 2, document.GetByKey("refusedEnvironments").Count(),
			"a refused patch must not discard the refusals already reported")

		// An environment that goes away entirely stops being reported as refused.
		actions.DeleteEnvironment("env-bravo")

		document = fetchStatusDocument(t, p.relay)
		refused = document.GetByKey("refusedEnvironments")
		require.Equal(t, 1, refused.Count())
		assert.Equal(t, "env-zulu", refused.GetByIndex(0).GetByKey("envId").StringValue())
	})
}

func TestStatusReportsAnEmptyRefusedListWhenNothingIsRefused(t *testing.T) {
	// The field is always present, so a monitor can address it without having to tell an empty list
	// apart from a Relay too old to report one.
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		document := fetchStatusDocument(t, p.relay)
		refused := document.GetByKey("refusedEnvironments")
		assert.True(t, refused.IsDefined(), "refusedEnvironments must always be present")
		assert.Equal(t, 0, refused.Count())
	})
}

func TestARefusedUpdateForAServedEnvironmentSaysItIsStillServing(t *testing.T) {
	// The two ways into this list need different responses. An environment Relay never managed to
	// configure is an outage for it. An environment whose *update* was refused keeps serving the
	// credentials it already had, and appears under environments as well, so a monitor that treats
	// every entry as an outage would page for a working environment.
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvClientSide)

	withStartedRelay(t, config, func(p relayTestParams) {
		actions := &relayAutoConfigActions{r: p.relay}
		servedID := string(st.EnvClientSide.Config.EnvID)

		actions.EnvironmentRefused(c.EnvironmentID(servedID), "malformed credential payload")
		actions.EnvironmentRefused("env-never-configured", "malformed credential payload")

		document := fetchStatusDocument(t, p.relay)
		refused := document.GetByKey("refusedEnvironments")
		require.Equal(t, 2, refused.Count())

		byID := make(map[string]ldvalue.Value, refused.Count())
		for i := 0; i < refused.Count(); i++ {
			entry := refused.GetByIndex(i)
			byID[entry.GetByKey("envId").StringValue()] = entry
		}

		assert.True(t, byID[servedID].GetByKey("serving").BoolValue(),
			"an environment Relay still serves must not be reported as unserved")
		assert.False(t, byID["env-never-configured"].GetByKey("serving").BoolValue(),
			"an environment Relay has no configuration for is not being served")

		// The serving one is in both blocks at once, which is the whole reason the field exists.
		var alsoInEnvironments bool
		for _, entry := range document.GetByKey("environments").AsValueMap().AsMap() {
			if entry.GetByKey("envId").StringValue() == servedID {
				alsoInEnvironments = true
			}
		}
		assert.True(t, alsoInEnvironments,
			"a refused update must not remove the environment from the document")
	})
}
