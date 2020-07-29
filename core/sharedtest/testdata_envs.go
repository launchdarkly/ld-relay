package sharedtest

import (
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
)

type TestEnv struct {
	Name   string
	Config config.EnvConfig
}

type UnsupportedSDKCredential struct{} // implements config.SDKCredential

func (k UnsupportedSDKCredential) GetAuthorizationHeaderValue() string { return "" }

const (
	// The "undefined" values are well-formed, but do not match any environment in our test data.
	UndefinedSDKKey    = config.SDKKey("sdk-99999999-9999-4999-8999-999999999999")
	UndefinedMobileKey = config.MobileKey("mob-99999999-9999-4999-8999-999999999999")
	UndefinedEnvID     = config.EnvironmentID("999999999999999999999999")

	// The "malformed" values contain an unsupported authorization scheme.
	MalformedSDKKey    = config.SDKKey("fake_key sdk-99999999-9999-4999-8999-999999999999")
	MalformedMobileKey = config.MobileKey("fake_key mob-99999999-9999-4999-8999-999999999999")
)

var EnvMain = TestEnv{
	Name: "sdk test",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0"),
	},
}

var EnvWithTTL = TestEnv{
	Name: "sdk test with TTL",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d5"),
		TTL:    ct.NewOptDuration(10 * time.Minute),
	},
}

var EnvMobile = TestEnv{
	Name: "mobile test",
	Config: config.EnvConfig{
		SDKKey:    config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2"),
		MobileKey: config.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"),
	},
}

var EnvClientSide = TestEnv{
	Name: "JS client-side test",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1"),
		EnvID:  config.EnvironmentID("507f1f77bcf86cd799439011"),
	},
}

var EnvClientSideSecureMode = TestEnv{
	Name: "JS client-side test with secure mode",
	Config: config.EnvConfig{
		SDKKey:     config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d9"),
		EnvID:      config.EnvironmentID("507f1f77bcf86cd799439019"),
		SecureMode: true,
	},
}

func MakeEnvConfigs(envs ...TestEnv) map[string]*config.EnvConfig {
	ret := make(map[string]*config.EnvConfig)
	for _, e := range envs {
		c := e.Config
		ret[e.Name] = &c
	}
	return ret
}
