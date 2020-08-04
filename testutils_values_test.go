package relay

import (
	"encoding/json"
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	c "github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
)

type testEnv struct {
	name   string
	config config.EnvConfig
}

type unsupportedSDKCredential struct{} // implements config.SDKCredential

func (k unsupportedSDKCredential) GetAuthorizationHeaderValue() string { return "" }

// Returns a key matching the UUID header pattern
func key() config.MobileKey {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

const (
	// The "undefined" values are well-formed, but do not match any environment in our test data.
	undefinedSDKKey    = config.SDKKey("sdk-99999999-9999-4999-8999-999999999999")
	undefinedMobileKey = config.MobileKey("mob-99999999-9999-4999-8999-999999999999")
	undefinedEnvID     = config.EnvironmentID("999999999999999999999999")

	// The "malformed" values contain an unsupported authorization scheme.
	malformedSDKKey    = config.SDKKey("fake_key sdk-99999999-9999-4999-8999-999999999999")
	malformedMobileKey = config.MobileKey("fake_key mob-99999999-9999-4999-8999-999999999999")
)

var testEnvMain = testEnv{
	name: "sdk test",
	config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0"),
	},
}

var testEnvWithTTL = testEnv{
	name: "sdk test with TTL",
	config: config.EnvConfig{
		SDKKey: c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d5"),
		TTL:    ct.NewOptDuration(10 * time.Minute),
	},
}

var testEnvMobile = testEnv{
	name: "mobile test",
	config: config.EnvConfig{
		SDKKey:    c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2"),
		MobileKey: c.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"),
	},
}

var testEnvClientSide = testEnv{
	name: "JS client-side test",
	config: config.EnvConfig{
		SDKKey: c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1"),
		EnvID:  c.EnvironmentID("507f1f77bcf86cd799439011"),
	},
}

var testEnvClientSideSecureMode = testEnv{
	name: "JS client-side test with secure mode",
	config: config.EnvConfig{
		SDKKey:     c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d9"),
		EnvID:      c.EnvironmentID("507f1f77bcf86cd799439019"),
		SecureMode: true,
	},
}

func makeEnvConfigs(envs ...testEnv) map[string]*config.EnvConfig {
	ret := make(map[string]*config.EnvConfig)
	for _, e := range envs {
		c := e.config
		ret[e.name] = &c
	}
	return ret
}

func flagsMap(testFlags []st.TestFlag) map[string]interface{} {
	ret := make(map[string]interface{})
	for _, f := range testFlags {
		ret[f.Flag.Key] = f.Flag
	}
	return ret
}

func makeEvalBody(flags []st.TestFlag, fullData bool, reasons bool) string {
	obj := make(map[string]interface{})
	for _, f := range flags {
		value := f.ExpectedValue
		if fullData {
			m := map[string]interface{}{"value": value, "version": f.Flag.Version}
			if value != nil {
				m["variation"] = f.ExpectedVariation
			}
			if reasons || f.IsExperiment {
				m["reason"] = f.ExpectedReason
			}
			if f.Flag.TrackEvents || f.IsExperiment {
				m["trackEvents"] = true
			}
			if f.IsExperiment {
				m["trackReason"] = true
			}
			value = m
		}
		obj[f.Flag.Key] = value
	}
	out, _ := json.Marshal(obj)
	return string(out)
}
