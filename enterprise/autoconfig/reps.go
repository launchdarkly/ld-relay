package autoconfig

import (
	config "github.com/launchdarkly/ld-relay-config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

// These representation types are exported so that tests in other packages can more easily create
// simulated auto-config data. They should not be used by non-test code in other packages.

// EnvironmentRep is a representation of an environment that is being added or updated (in either a "put"
// or a "patch" message).
type EnvironmentRep struct {
	EnvID      config.EnvironmentID `json:"envID"`
	EnvKey     string               `json:"envKey"`
	EnvName    string               `json:"envName"`
	MobKey     config.MobileKey     `json:"mobKey"`
	ProjKey    string               `json:"projKey"`
	ProjName   string               `json:"projName"`
	SDKKey     SDKKeyRep            `json:"sdkKey"`
	DefaultTTL int                  `json:"defaultTtl"`
	SecureMode bool                 `json:"secureMode"`
	Version    int                  `json:"version"`
}

// SDKKeyRep describes an SDK key optionally accompanied by an old expiring key.
type SDKKeyRep struct {
	Value    config.SDKKey `json:"value"`
	Expiring ExpiringKeyRep
}

// ExpiringKeyRep describes an old key that will expire at the specified date/time.
type ExpiringKeyRep struct {
	Value     config.SDKKey              `json:"value"`
	Timestamp ldtime.UnixMillisecondTime `json:"timestamp"`
}
