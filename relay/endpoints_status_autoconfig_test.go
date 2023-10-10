package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
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

func transformEnvConfigsToAutoConfig(config c.Config) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{Path: "/", Data: autoconfig.PutContent{
		Environments: make(map[c.EnvironmentID]envfactory.EnvironmentRep)}}
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
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(envConfig.ExpiringSDKKey)),
				status, "environments", envKey, "expiringSdkKey")
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

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	r, err := newRelayInternal(entConfig, relayInternalOptions{
		loggers:       mockLog.Loggers,
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
