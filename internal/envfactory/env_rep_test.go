package envfactory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentRepToParams(t *testing.T) {
	env1 := EnvironmentRep{
		EnvID:      config.EnvironmentID("envid1"),
		EnvKey:     "envkey1",
		EnvName:    "envname1",
		MobKey:     config.MobileKey("mobkey1"),
		ProjKey:    "projkey1",
		ProjName:   "projname1",
		SDKKey:     SDKKeyRep{Value: config.SDKKey("sdkkey1")},
		DefaultTTL: 2,
		SecureMode: true,
	}
	params1 := env1.ToParams()
	assert.Equal(t, EnvironmentParams{
		EnvID: env1.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   "envkey1",
			EnvName:  "envname1",
			ProjKey:  "projkey1",
			ProjName: "projname1",
		},
		SDKKey:     env1.SDKKey.Value,
		MobileKey:  env1.MobKey,
		TTL:        2 * time.Minute,
		SecureMode: true,
	}, params1)

	env2 := EnvironmentRep{
		EnvID:    config.EnvironmentID("envid2"),
		EnvKey:   "envkey2",
		EnvName:  "envname2",
		MobKey:   config.MobileKey("mobkey2"),
		ProjKey:  "projkey2",
		ProjName: "projname2",
		SDKKey: SDKKeyRep{
			Value: config.SDKKey("sdkkey2"),
			Expiring: ExpiringKeyRep{
				Value:     config.SDKKey("oldkey"),
				Timestamp: ldtime.UnixMillisecondTime(10000),
			}},
	}
	params2 := env2.ToParams()
	assert.Equal(t, EnvironmentParams{
		EnvID: env2.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   "envkey2",
			EnvName:  "envname2",
			ProjKey:  "projkey2",
			ProjName: "projname2",
		},
		SDKKey: env2.SDKKey.Value,
		ExpiringSDKKey: ExpiringSDKKey{
			Key:        env2.SDKKey.Expiring.Value,
			Expiration: time.UnixMilli(int64(env2.SDKKey.Expiring.Timestamp)),
		},
		MobileKey: env2.MobKey,
	}, params2)
}

func TestEnvironmentRepToParamsAdditionalSDKKeys(t *testing.T) {
	expiresAt := ldtime.UnixMillisecondTime(20000)
	env := EnvironmentRep{
		EnvID:  config.EnvironmentID("envid"),
		MobKey: config.MobileKey("mob"),
		SDKKey: SDKKeyRep{
			Value: config.SDKKey("primary"),
			Additional: []AdditionalSDKKeyRep{
				{Value: config.SDKKey("active1")},
				{Value: config.SDKKey("active2")},
				{Value: config.SDKKey("expiring1"), ExpiresAt: &expiresAt},
			},
		},
	}

	params := env.ToParams()

	assert.Equal(t, []config.SDKKey{"active1", "active2"}, params.AdditionalSDKKeys)
	assert.Equal(t, map[config.SDKKey]time.Time{
		"expiring1": time.UnixMilli(int64(expiresAt)),
	}, params.ExpiringAdditionalSDKKeys)
}

func TestEnvironmentRepToParamsAdditionalSDKKeysSkipsUndefinedEntries(t *testing.T) {
	env := EnvironmentRep{
		SDKKey: SDKKeyRep{
			Value: config.SDKKey("primary"),
			Additional: []AdditionalSDKKeyRep{
				{Value: config.SDKKey("")},
				{Value: config.SDKKey("active")},
			},
		},
	}

	params := env.ToParams()

	assert.Equal(t, []config.SDKKey{"active"}, params.AdditionalSDKKeys)
	assert.Nil(t, params.ExpiringAdditionalSDKKeys)
}

func TestEnvironmentRepToParamsMobileKeyPrefersNewField(t *testing.T) {
	env := EnvironmentRep{
		EnvID:  config.EnvironmentID("envid"),
		MobKey: config.MobileKey("legacy"),
		MobileKey: &MobileKeyRep{
			Value: config.MobileKey("primary"),
		},
	}

	params := env.ToParams()

	assert.Equal(t, config.MobileKey("primary"), params.MobileKey)
}

func TestEnvironmentRepToParamsMobileKeyEmptyValueFallsBackToLegacy(t *testing.T) {
	// Regression test for the F11 finding: a partially-populated MobileKey struct (e.g., during a
	// platform rollout where the new field exists but the value isn't filled in) must not clobber
	// the legacy MobKey field.
	env := EnvironmentRep{
		MobKey: config.MobileKey("legacy"),
		MobileKey: &MobileKeyRep{
			Value: config.MobileKey(""), // not defined
		},
	}

	params := env.ToParams()
	assert.Equal(t, config.MobileKey("legacy"), params.MobileKey)
}

func TestEnvironmentRepToParamsMobileKeyFallsBackToLegacyField(t *testing.T) {
	env := EnvironmentRep{
		EnvID:  config.EnvironmentID("envid"),
		MobKey: config.MobileKey("legacy"),
	}

	params := env.ToParams()

	assert.Equal(t, config.MobileKey("legacy"), params.MobileKey)
	assert.False(t, params.ExpiringMobileKey.Defined())
	assert.Nil(t, params.AdditionalMobileKeys)
	assert.Nil(t, params.ExpiringAdditionalMobileKeys)
}

func TestEnvironmentRepToParamsExpiringMobileKey(t *testing.T) {
	env := EnvironmentRep{
		MobileKey: &MobileKeyRep{
			Value: config.MobileKey("primary"),
			Expiring: ExpiringMobileKeyRep{
				Value:     config.MobileKey("oldprimary"),
				Timestamp: ldtime.UnixMillisecondTime(30000),
			},
		},
	}

	params := env.ToParams()

	assert.Equal(t, ExpiringMobileKey{
		Key:        config.MobileKey("oldprimary"),
		Expiration: time.UnixMilli(30000),
	}, params.ExpiringMobileKey)
}

func TestEnvironmentRepToParamsAdditionalMobileKeys(t *testing.T) {
	expiresAt := ldtime.UnixMillisecondTime(40000)
	env := EnvironmentRep{
		MobileKey: &MobileKeyRep{
			Value: config.MobileKey("primary"),
			Additional: []AdditionalMobileKeyRep{
				{Value: config.MobileKey("active1")},
				{Value: config.MobileKey("expiring1"), ExpiresAt: &expiresAt},
			},
		},
	}

	params := env.ToParams()

	assert.Equal(t, []config.MobileKey{"active1"}, params.AdditionalMobileKeys)
	assert.Equal(t, map[config.MobileKey]time.Time{
		"expiring1": time.UnixMilli(int64(expiresAt)),
	}, params.ExpiringAdditionalMobileKeys)
}

func TestEnvironmentRepJSONFormat(t *testing.T) {
	jsonStr := `{
		"envID": "envid1",
		"envKey": "envkey",
		"envName": "envname",
		"mobKey": "mobkey",
		"projKey": "projkey",
		"projName": "projname",
		"sdkKey": { "value": "sdkkey", "expiring": { "value": "oldkey", "timestamp": 10000 } },
		"defaultTtl": 2,
		"secureMode": true
	  }`
	var rep EnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep))
	assert.Equal(t, EnvironmentRep{
		EnvID:    config.EnvironmentID("envid1"),
		EnvKey:   "envkey",
		EnvName:  "envname",
		MobKey:   config.MobileKey("mobkey"),
		ProjKey:  "projkey",
		ProjName: "projname",
		SDKKey: SDKKeyRep{
			Value: config.SDKKey("sdkkey"),
			Expiring: ExpiringKeyRep{
				Value:     config.SDKKey("oldkey"),
				Timestamp: ldtime.UnixMillisecondTime(10000),
			},
		},
		DefaultTTL: 2,
		SecureMode: true,
	}, rep)
}

func TestEnvironmentRepJSONFormatWithAdditionalKeys(t *testing.T) {
	jsonStr := `{
		"envID": "envid",
		"envKey": "envkey",
		"envName": "envname",
		"mobKey": "mob-legacy",
		"mobileKey": {
			"value": "mob-primary",
			"expiring": { "value": "mob-old", "timestamp": 5000 },
			"additional": [
				{ "value": "mob-extra-1" },
				{ "value": "mob-extra-2", "expiresAt": 9000 }
			]
		},
		"projKey": "projkey",
		"projName": "projname",
		"sdkKey": {
			"value": "sdk-primary",
			"expiring": { "value": "sdk-old", "timestamp": 6000 },
			"additional": [
				{ "value": "sdk-extra-1" },
				{ "value": "sdk-extra-2", "expiresAt": 8000 }
			]
		}
	  }`
	var rep EnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep))

	expiresMob := ldtime.UnixMillisecondTime(9000)
	expiresSDK := ldtime.UnixMillisecondTime(8000)
	assert.Equal(t, EnvironmentRep{
		EnvID:    config.EnvironmentID("envid"),
		EnvKey:   "envkey",
		EnvName:  "envname",
		MobKey:   config.MobileKey("mob-legacy"),
		ProjKey:  "projkey",
		ProjName: "projname",
		MobileKey: &MobileKeyRep{
			Value: config.MobileKey("mob-primary"),
			Expiring: ExpiringMobileKeyRep{
				Value:     config.MobileKey("mob-old"),
				Timestamp: ldtime.UnixMillisecondTime(5000),
			},
			Additional: []AdditionalMobileKeyRep{
				{Value: config.MobileKey("mob-extra-1")},
				{Value: config.MobileKey("mob-extra-2"), ExpiresAt: &expiresMob},
			},
		},
		SDKKey: SDKKeyRep{
			Value: config.SDKKey("sdk-primary"),
			Expiring: ExpiringKeyRep{
				Value:     config.SDKKey("sdk-old"),
				Timestamp: ldtime.UnixMillisecondTime(6000),
			},
			Additional: []AdditionalSDKKeyRep{
				{Value: config.SDKKey("sdk-extra-1")},
				{Value: config.SDKKey("sdk-extra-2"), ExpiresAt: &expiresSDK},
			},
		},
	}, rep)
}
