package envfactory

import (
	"fmt"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
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

type FilterRep struct {
	ProjKey   string           `json:"projKey"`
	FilterKey config.FilterKey `json:"key"`
	Version   int              `json:"version"`
}

// ToParams converts the JSON properties for a filter into our internal parameter type. It requires an
// explicit FilterID because there is no ID included within the JSON representation.
func (f FilterRep) ToParams(id config.FilterID) FilterParams {
	return FilterParams{
		ProjKey: f.ProjKey,
		Key:     f.FilterKey,
		ID:      id,
	}
}

// ToTestParams is similar to ToParams, but intended as a convenience for tests. It assumes that
// a filter's ID can be computed by concatenating the project key with the filter key.
func (f FilterRep) ToTestParams() FilterParams {
	return f.ToParams(config.FilterID(fmt.Sprintf("%s.%s", f.ProjKey, f.FilterKey)))
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

func ToTime(millisecondTime ldtime.UnixMillisecondTime) time.Time {
	return time.UnixMilli(int64(millisecondTime))
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
		SDKKey:    r.SDKKey.Value,
		MobileKey: r.MobKey,
		ExpiringSDKKey: ExpiringSDKKey{
			Key:        r.SDKKey.Expiring.Value,
			Expiration: ToTime(r.SDKKey.Expiring.Timestamp),
		},
		TTL:        time.Duration(r.DefaultTTL) * time.Minute,
		SecureMode: r.SecureMode,
	}
}

func (r EnvironmentRep) Describe() string {
	return fmt.Sprintf("environment %s (%s %s)", r.EnvID, r.ProjName, r.EnvName)
}

func (r EnvironmentRep) ID() string {
	return string(r.EnvID)
}

func (f FilterRep) Describe() string {
	return fmt.Sprintf("filter %s (%s)", f.FilterKey, f.ProjKey)
}
