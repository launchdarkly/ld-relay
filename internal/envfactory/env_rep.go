package envfactory

import (
	"fmt"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
)

// These representation types are used by both the autoconfig package and the filedata package,
// because the base properties for environments in both the auto-configuration protocol and the
// file data source archive are deliberately the same. Any properties that are only used in one
// or the other of those contexts should be in the appropriate package instead of here.

// EnvironmentRep is the wire shape of an environment, shared by RAC and the offline archive.
//
// Wire vocabulary: "key" is the non-secret human-readable identifier; "value" is the credential
// secret. Relay's own SDKKey, MobileKey, and SDKCredential types hold what the wire calls "value".
// Do not rename them.
//
// sdkKey and mobKey are the singular default credentials. sdkKey is an object because it also carries
// the legacy sdkKey.expiring slot; mobKey is a plain string because mobile keys never had one.
// sdkKeys and mobileKeys are the authoritative full accepted set, with entries of the form
// { key, value, expiry?, hasViews? }.
//
// This parse path never sets DisallowUnknownFields, so a relay predating concurrent keys ignores
// sdkKeys and mobileKeys and keeps using sdkKey and mobKey.
type EnvironmentRep struct {
	EnvID      config.EnvironmentID `json:"envID"`
	EnvKey     string               `json:"envKey"`
	EnvName    string               `json:"envName"`
	MobKey     config.MobileKey     `json:"mobKey"`
	ProjKey    string               `json:"projKey"`
	ProjName   string               `json:"projName"`
	SDKKey     SDKKeyRep            `json:"sdkKey"`
	SDKKeys    []ConcurrentKeyRep   `json:"sdkKeys,omitempty"`
	MobileKeys []ConcurrentKeyRep   `json:"mobileKeys,omitempty"`
	DefaultTTL int                  `json:"defaultTtl"`
	SecureMode bool                 `json:"secureMode"`
	Version    int                  `json:"version"`
}

// ConcurrentKeyRep is an entry in the sdkKeys or mobileKeys array on EnvironmentRep. It represents one
// accepted credential in an environment's concurrent key set. Refer to EnvironmentRep for the wire
// vocabulary.
type ConcurrentKeyRep struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Expiry   *int64 `json:"expiry,omitempty"` // Unix-ms; nil = permanent
	HasViews bool   `json:"hasViews"`
}

// SDKKeyRep describes an SDK key optionally accompanied by an old expiring key.
type SDKKeyRep struct {
	Value    config.SDKKey  `json:"value"`
	Expiring ExpiringKeyRep `json:"expiring"`
}

// ExpiringKeyRep describes an old key that will expire at the specified date/time.
type ExpiringKeyRep struct {
	Value     config.SDKKey              `json:"value"`
	Timestamp ldtime.UnixMillisecondTime `json:"timestamp"`
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

func ToTime(millisecondTime ldtime.UnixMillisecondTime) time.Time {
	return time.UnixMilli(int64(millisecondTime)) //nolint: gosec
}

// ToParams converts the JSON properties for an environment into our internal parameter type.
func (r EnvironmentRep) ToParams() EnvironmentParams {
	params := EnvironmentParams{
		EnvID: r.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   r.EnvKey,
			EnvName:  r.EnvName,
			ProjKey:  r.ProjKey,
			ProjName: r.ProjName,
		},
		SDKKey:         r.SDKKey.Value,
		ExpiringSDKKey: r.SDKKey.Expiring.ToParams(),
		MobileKey:      r.MobKey,
		TTL:            time.Duration(r.DefaultTTL) * time.Minute,
		SecureMode:     r.SecureMode,
	}

	if len(r.SDKKeys) > 0 {
		// New-format payload: populate directly from the array.
		params.AcceptedSDKKeys = make([]AcceptedSDKKey, 0, len(r.SDKKeys))
		for _, k := range r.SDKKeys {
			entry := AcceptedSDKKey{
				Key:      k.Key,
				Value:    config.SDKKey(k.Value),
				HasViews: k.HasViews,
			}
			if k.Expiry != nil {
				entry.Expiry = time.UnixMilli(*k.Expiry)
			}
			params.AcceptedSDKKeys = append(params.AcceptedSDKKeys, entry)
		}
	} else {
		// Old-format payload: synthesize AcceptedSDKKeys from the singular sdkKey fields, so the model
		// is always non-nil. The old format carried no identifier, so Key stays empty.
		params.AcceptedSDKKeys = make([]AcceptedSDKKey, 0, 2)
		params.AcceptedSDKKeys = append(params.AcceptedSDKKeys, AcceptedSDKKey{Value: r.SDKKey.Value})
		if r.SDKKey.Expiring.Value.Defined() {
			params.AcceptedSDKKeys = append(params.AcceptedSDKKeys, AcceptedSDKKey{
				Value:  r.SDKKey.Expiring.Value,
				Expiry: ToTime(r.SDKKey.Expiring.Timestamp),
			})
		}
	}

	if len(r.MobileKeys) > 0 {
		// New-format payload: populate directly from the array.
		params.AcceptedMobileKeys = make([]AcceptedMobileKey, 0, len(r.MobileKeys))
		for _, k := range r.MobileKeys {
			entry := AcceptedMobileKey{
				Key:      k.Key,
				Value:    config.MobileKey(k.Value),
				HasViews: k.HasViews,
			}
			if k.Expiry != nil {
				entry.Expiry = time.UnixMilli(*k.Expiry)
			}
			params.AcceptedMobileKeys = append(params.AcceptedMobileKeys, entry)
		}
	} else {
		// Old-format payload: synthesize from the singular mobKey field. An undefined mobKey means the
		// environment has no mobile key, so leave the set empty. A phantom empty-value entry would make
		// BuildAcceptedSet reject the payload.
		params.AcceptedMobileKeys = []AcceptedMobileKey{}
		if r.MobKey.Defined() {
			params.AcceptedMobileKeys = append(params.AcceptedMobileKeys, AcceptedMobileKey{Value: r.MobKey})
		}
	}

	return params
}

func (r EnvironmentRep) Describe() string {
	return fmt.Sprintf("environment %s (%s %s)", r.EnvID, r.ProjName, r.EnvName)
}

func (r EnvironmentRep) ID() string {
	return string(r.EnvID)
}
