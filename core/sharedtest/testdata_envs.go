package sharedtest

import (
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/core/config"
)

type TestEnv struct {
	Name               string
	Config             config.EnvConfig
	ProjName           string
	ProjKey            string
	EnvName            string
	EnvKey             string
	ExpiringSDKKey     config.SDKKey
	ExpiringSDKKeyTime ldtime.UnixMillisecondTime
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
	Name: "ProjectName ServerSideEnv",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0"),
	},
}

var EnvWithTTL = TestEnv{
	Name: "ProjectName ServerSideEnvWithTTL",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d5"),
		TTL:    ct.NewOptDuration(10 * time.Minute),
	},
}

var EnvMobile = TestEnv{
	Name: "ProjectName MobileEnv",
	Config: config.EnvConfig{
		SDKKey:    config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2"),
		MobileKey: config.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"),
	},
}

var EnvClientSide = TestEnv{
	Name: "ProjectName JSClientSideEnv",
	Config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1"),
		EnvID:  config.EnvironmentID("507f1f77bcf86cd799439011"),
	},
}

var EnvClientSideSecureMode = TestEnv{
	Name: "ProjectName JSClientSideSecureModeEnv",
	Config: config.EnvConfig{
		SDKKey:     config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d9"),
		EnvID:      config.EnvironmentID("507f1f77bcf86cd799439019"),
		SecureMode: true,
	},
}

var EnvWithAllCredentials = TestEnv{
	Name: "ProjectName EnvWithAllCredentials",
	Config: config.EnvConfig{
		SDKKey:    config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-2e1e4d8e52e9"),
		MobileKey: config.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42ec"),
		EnvID:     config.EnvironmentID("507f1f77bcf86cd79943902a"),
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
