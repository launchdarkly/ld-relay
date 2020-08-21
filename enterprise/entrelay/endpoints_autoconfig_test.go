package entrelay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay-config"
	core "github.com/launchdarkly/ld-relay-core"
	st "github.com/launchdarkly/ld-relay-core/sharedtest"
	"github.com/launchdarkly/ld-relay-core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay-core/sharedtest/testsuites"
	"github.com/launchdarkly/ld-relay/v6/enterprise/autoconfig"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"

	"github.com/stretchr/testify/assert"
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

// Unlike endpoints_generic_test.go, which runs with a local configuration like the regular Relay tests,
// here we are testing endpoint responses for a Relay Proxy Enterprise instance that is auto-configured.
// We don't run the full core test suite this way, since most things behave the same with or without
// auto-config once the environment list has been obtained; we just want to make sure it starts up
// correctly in general and test for any specific responses that should be different.

func relayForEndpointTestsWithAutoConfig(config c.Config) testsuites.TestParams {
	autoConfigEvent := transformEnvConfigsToAutoConfig(config)
	autoConfigHandler, _ := httphelpers.SSEHandler(&autoConfigEvent)
	server := httptest.NewServer(autoConfigHandler)

	entConfig := entconfig.EnterpriseConfig{Config: config}
	entConfig.AutoConfig.Key = testAutoConfKey
	entConfig.Environment = nil
	entConfig.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)

	r, err := NewRelayEnterprise(entConfig, ldlog.NewDisabledLoggers(), testclient.CreateDummyClient)
	if err != nil {
		panic(err)
	}
	waitForAutoConfigInit(r, config)

	return testsuites.TestParams{
		Core:    r.core,
		Handler: r.handler,
		Closer: func() {
			r.Close()
			server.Close()
		},
	}
}

func TestRelayEnterpriseCoreEndpointsWithAutoConfig(t *testing.T) {
	// We don't run the entire test suite in auto-config mode, since most things behave the same way they do
	// without auto-config once the environment list has been obtained.
	ctor := testsuites.TestConstructor(relayForEndpointTestsWithAutoConfig)
	ctor.RunTest(t, "status", DoAutoConfigStatusEndpointTests)
}

func transformEnvConfigsToAutoConfig(config c.Config) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{Path: "/", Data: autoconfig.PutContent{
		Environments: make(map[c.EnvironmentID]autoconfig.EnvironmentRep)}}
	for _, envConfig := range config.Environment {
		env, ok := autoConfigTestEnvs[envConfig.EnvID]
		if !ok {
			panic("can't run auto-config with an environment that's not in autoConfigTestEnvs")
		}
		rep := autoconfig.EnvironmentRep{
			EnvID:    env.Config.EnvID,
			EnvKey:   env.EnvKey,
			EnvName:  env.EnvName,
			ProjKey:  env.ProjKey,
			ProjName: env.ProjName,
			MobKey:   env.Config.MobileKey,
			SDKKey: autoconfig.SDKKeyRep{
				Value: env.Config.SDKKey,
			},
		}
		if env.ExpiringSDKKey != "" {
			rep.SDKKey.Expiring.Value = env.ExpiringSDKKey
			rep.SDKKey.Expiring.Timestamp = ldtime.UnixMillisNow() + env.ExpiringSDKKeyTime
		}
		data.Data.Environments[env.Config.EnvID] = rep
	}
	jsonData, _ := json.Marshal(data)
	return httphelpers.SSEEvent{Event: autoconfig.PutEvent, Data: string(jsonData)}
}

func waitForAutoConfigInit(r *RelayEnterprise, configWithEnvs c.Config) {
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
			envs := r.core.GetAllEnvironments()
			if len(envs) == expectedEnvCount {
				return
			}
		case <-deadline:
			panic("timed out waiting for auto-configuration to happen")
		}
	}
}

func DoAutoConfigStatusEndpointTests(t *testing.T, constructor testsuites.TestConstructor) {
	t.Run("basic status properties", func(t *testing.T) {
		envConfig := testEnvBasic
		config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
		testsuites.DoTest(config, constructor, func(p testsuites.TestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.Handler)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			envKey := string(envConfig.Config.EnvID)

			st.AssertJSONPathMatch(t, envKey,
				status, "environments", envKey, "envId")
			st.AssertJSONPathMatch(t, core.ObscureKey(string(envConfig.Config.SDKKey)),
				status, "environments", envKey, "sdkKey")
			st.AssertJSONPathMatch(t, core.ObscureKey(string(envConfig.Config.MobileKey)),
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
			st.AssertJSONPathMatch(t, p.Core.Version, status, "version")
			st.AssertJSONPathMatch(t, ld.Version, status, "clientVersion")
		})
	})

	t.Run("expiring SDK key", func(t *testing.T) {
		envConfig := testEnvWithExpiringKey
		config := c.Config{Environment: st.MakeEnvConfigs(envConfig)}
		testsuites.DoTest(config, constructor, func(p testsuites.TestParams) {
			r, _ := http.NewRequest("GET", "http://localhost/status", nil)
			result, body := st.DoRequest(r, p.Handler)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			envKey := string(envConfig.Config.EnvID)

			st.AssertJSONPathMatch(t, envKey,
				status, "environments", envKey, "envId")
			st.AssertJSONPathMatch(t, core.ObscureKey(string(envConfig.Config.SDKKey)),
				status, "environments", envKey, "sdkKey")
			st.AssertJSONPathMatch(t, core.ObscureKey(string(envConfig.ExpiringSDKKey)),
				status, "environments", envKey, "expiringSdkKey")
		})
	})
}
