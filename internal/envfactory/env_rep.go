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
//
// MobKey is the legacy single-mobile-key field. When MobileKey is non-nil it takes precedence;
// MobKey is retained so older relays reading newer payloads continue to function.
type EnvironmentRep struct {
	EnvID      config.EnvironmentID `json:"envID"`
	EnvKey     string               `json:"envKey"`
	EnvName    string               `json:"envName"`
	MobKey     config.MobileKey     `json:"mobKey,omitempty"`
	MobileKey  *MobileKeyRep        `json:"mobileKey,omitempty"`
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

// SDKKeyRep describes an SDK key, an optional predecessor that is rotating out with a grace period,
// and an optional list of additional concurrent SDK keys.
//
// Each additional key may carry its own ExpiresAt. Entries without ExpiresAt are active; entries
// with ExpiresAt are in a per-key grace period and stop being honored after the timestamp.
// Omission of a previously-listed additional key indicates immediate revocation.
type SDKKeyRep struct {
	Value      config.SDKKey         `json:"value"`
	Expiring   ExpiringKeyRep        `json:"expiring"`
	Additional []AdditionalSDKKeyRep `json:"additional,omitempty"`
}

// ExpiringKeyRep describes an old key that will expire at the specified date/time.
type ExpiringKeyRep struct {
	Value     config.SDKKey              `json:"value"`
	Timestamp ldtime.UnixMillisecondTime `json:"timestamp"`
}

// AdditionalSDKKeyRep describes a concurrent SDK key with an optional per-key expiry.
type AdditionalSDKKeyRep struct {
	Value     config.SDKKey               `json:"value"`
	ExpiresAt *ldtime.UnixMillisecondTime `json:"expiresAt,omitempty"`
}

// MobileKeyRep describes a mobile key, an optional predecessor that is rotating out with a grace
// period, and an optional list of additional concurrent mobile keys. Semantics mirror SDKKeyRep.
type MobileKeyRep struct {
	Value      config.MobileKey         `json:"value"`
	Expiring   ExpiringMobileKeyRep     `json:"expiring"`
	Additional []AdditionalMobileKeyRep `json:"additional,omitempty"`
}

// ExpiringMobileKeyRep describes an old mobile key that will expire at the specified date/time.
type ExpiringMobileKeyRep struct {
	Value     config.MobileKey           `json:"value"`
	Timestamp ldtime.UnixMillisecondTime `json:"timestamp"`
}

// AdditionalMobileKeyRep describes a concurrent mobile key with an optional per-key expiry.
type AdditionalMobileKeyRep struct {
	Value     config.MobileKey            `json:"value"`
	ExpiresAt *ldtime.UnixMillisecondTime `json:"expiresAt,omitempty"`
}

func (e ExpiringKeyRep) ToParams() ExpiringSDKKey {
	if e.Value.Defined() {
		return ExpiringSDKKey{
			Key:        e.Value,
			Expiration: ToTime(e.Timestamp),
		}
	} else {
		return ExpiringSDKKey{}
	}
}

func (e ExpiringMobileKeyRep) ToParams() ExpiringMobileKey {
	if e.Value.Defined() {
		return ExpiringMobileKey{
			Key:        e.Value,
			Expiration: ToTime(e.Timestamp),
		}
	}
	return ExpiringMobileKey{}
}

func ToTime(millisecondTime ldtime.UnixMillisecondTime) time.Time {
	return time.UnixMilli(int64(millisecondTime)) //nolint: gosec
}

// ToParams converts the JSON properties for an environment into our internal parameter type.
func (r EnvironmentRep) ToParams() EnvironmentParams {
	activeSDK, expiringAdditionalSDK := splitAdditionalSDKKeys(r.SDKKey.Additional)

	mobileKey := r.MobKey
	var expiringMobile ExpiringMobileKey
	var activeMobile []config.MobileKey
	var expiringAdditionalMobile map[config.MobileKey]time.Time
	if r.MobileKey != nil {
		// Defensive: a partially-populated MobileKey struct with an empty Value should not clobber
		// the legacy MobKey field that an older platform serializer might have populated.
		if r.MobileKey.Value.Defined() {
			mobileKey = r.MobileKey.Value
		}
		expiringMobile = r.MobileKey.Expiring.ToParams()
		activeMobile, expiringAdditionalMobile = splitAdditionalMobileKeys(r.MobileKey.Additional)
	}

	return EnvironmentParams{
		EnvID: r.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   r.EnvKey,
			EnvName:  r.EnvName,
			ProjKey:  r.ProjKey,
			ProjName: r.ProjName,
		},
		SDKKey:                       r.SDKKey.Value,
		ExpiringSDKKey:               r.SDKKey.Expiring.ToParams(),
		AdditionalSDKKeys:            activeSDK,
		ExpiringAdditionalSDKKeys:    expiringAdditionalSDK,
		MobileKey:                    mobileKey,
		ExpiringMobileKey:            expiringMobile,
		AdditionalMobileKeys:         activeMobile,
		ExpiringAdditionalMobileKeys: expiringAdditionalMobile,
		TTL:                          time.Duration(r.DefaultTTL) * time.Minute,
		SecureMode:                   r.SecureMode,
	}
}

func splitAdditionalSDKKeys(additional []AdditionalSDKKeyRep) (active []config.SDKKey, expiring map[config.SDKKey]time.Time) {
	// Defense-in-depth dedupe of duplicate entries in the wire-format Additional list. The
	// platform shouldn't send duplicates, but if it does we want deterministic results: expiring
	// wins over active (it carries a stronger signal), and within a kind the first occurrence
	// wins.
	var hasExpiring map[config.SDKKey]struct{}
	for _, a := range additional {
		if a.Value.Defined() && a.ExpiresAt != nil {
			if hasExpiring == nil {
				hasExpiring = make(map[config.SDKKey]struct{})
			}
			hasExpiring[a.Value] = struct{}{}
		}
	}

	seenActive := make(map[config.SDKKey]struct{})
	for _, a := range additional {
		if !a.Value.Defined() {
			continue
		}
		if a.ExpiresAt != nil {
			if expiring == nil {
				expiring = make(map[config.SDKKey]time.Time)
			}
			if _, dup := expiring[a.Value]; !dup {
				expiring[a.Value] = ToTime(*a.ExpiresAt)
			}
			continue
		}
		if _, isExp := hasExpiring[a.Value]; isExp {
			continue
		}
		if _, dup := seenActive[a.Value]; dup {
			continue
		}
		seenActive[a.Value] = struct{}{}
		active = append(active, a.Value)
	}
	return active, expiring
}

func splitAdditionalMobileKeys(additional []AdditionalMobileKeyRep) (active []config.MobileKey, expiring map[config.MobileKey]time.Time) {
	// Same dedupe semantics as splitAdditionalSDKKeys; see the comment there.
	var hasExpiring map[config.MobileKey]struct{}
	for _, a := range additional {
		if a.Value.Defined() && a.ExpiresAt != nil {
			if hasExpiring == nil {
				hasExpiring = make(map[config.MobileKey]struct{})
			}
			hasExpiring[a.Value] = struct{}{}
		}
	}

	seenActive := make(map[config.MobileKey]struct{})
	for _, a := range additional {
		if !a.Value.Defined() {
			continue
		}
		if a.ExpiresAt != nil {
			if expiring == nil {
				expiring = make(map[config.MobileKey]time.Time)
			}
			if _, dup := expiring[a.Value]; !dup {
				expiring[a.Value] = ToTime(*a.ExpiresAt)
			}
			continue
		}
		if _, isExp := hasExpiring[a.Value]; isExp {
			continue
		}
		if _, dup := seenActive[a.Value]; dup {
			continue
		}
		seenActive[a.Value] = struct{}{}
		active = append(active, a.Value)
	}
	return active, expiring
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
