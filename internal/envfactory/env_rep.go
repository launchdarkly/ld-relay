package envfactory

import (
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/relayenv"
)

// These representation types are used by both the autoconfig package and the filedata package,
// because the base properties for environments in both the auto-configuration protocol and the
// file data source archive are deliberately the same. Any properties that are only used in one
// or the other of those contexts should be in the appropriate package instead of here.

// EnvironmentRep is a representation of an environment that is being added or updated.
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

// ToParams converts the JSON properties for an environment into our internal parameter type.
func (r EnvironmentRep) ToParams() EnvironmentParams {
	return EnvironmentParams{
		EnvID: r.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   r.EnvKey,
			EnvName:  r.EnvName,
			ProjKey:  r.ProjKey,
			ProjName: r.ProjName,
		},
		SDKKey:         r.SDKKey.Value,
		MobileKey:      r.MobKey,
		ExpiringSDKKey: r.SDKKey.Expiring.Value,
		TTL:            time.Duration(r.DefaultTTL) * time.Minute,
		SecureMode:     r.SecureMode,
	}
}
