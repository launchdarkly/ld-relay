package autoconfig

import (
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

// Representation of an environment that is being added or updated (in either a "put" or a
// "patch" message).
type environmentRep struct {
	EnvID      config.EnvironmentID `json:"envID"`
	EnvKey     string               `json:"envKey"`
	EnvName    string               `json:"envName"`
	MobKey     config.MobileKey     `json:"mobKey"`
	ProjKey    string               `json:"projKey"`
	ProjName   string               `json:"projName"`
	SDKKey     sdkKeyRep            `json:"sdkKey"`
	DefaultTTL int                  `json:"defaultTtl"`
	SecureMode bool                 `json:"secureMode"`
	Version    int                  `json:"version"`
}

// Description of an SDK key optionally accompanied by an old expiring key.
type sdkKeyRep struct {
	Value    config.SDKKey `json:"value"`
	Expiring expiringKeyRep
}

// An old key that will expire at the specified date/time.
type expiringKeyRep struct {
	Value     config.SDKKey              `json:"value"`
	Timestamp ldtime.UnixMillisecondTime `json:"timestamp"`
}
