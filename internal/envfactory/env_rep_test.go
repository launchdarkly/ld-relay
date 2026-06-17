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
	// Old-format env (no SDKKeys/MobileKeys set) → accepted-set fields must be nil, not empty slice.
	assert.Nil(t, params1.AcceptedSDKKeys)
	assert.Nil(t, params1.AcceptedMobileKeys)

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

// TestEnvironmentRepNewFormatWithArrays parses a realistic RAC put payload carrying the new
// sdkKeys/mobileKeys arrays and verifies the struct fields are populated correctly.
func TestEnvironmentRepNewFormatWithArrays(t *testing.T) {
	expiryMs := int64(1700000000000)
	jsonStr := `{
		"envID": "68e5179e8307e4099c277e2a",
		"envKey": "production",
		"envName": "Production",
		"mobKey": "mob-f41c",
		"projKey": "my-project",
		"projName": "My Project",
		"sdkKey": { "value": "sdk-anchor" },
		"sdkKeys": [
			{ "key": "default-sdk", "value": "sdk-anchor" },
			{ "key": "service-a",   "value": "sdk-service-a", "expiry": 1700000000000 }
		],
		"mobileKeys": [
			{ "key": "mob-key-1", "value": "mob-f41c" }
		],
		"secureMode": false,
		"version": 26
	}`

	var rep EnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep))

	assert.Equal(t, config.SDKKey("sdk-anchor"), rep.SDKKey.Value)
	assert.Equal(t, config.MobileKey("mob-f41c"), rep.MobKey)

	require.Len(t, rep.SDKKeys, 2)
	assert.Equal(t, ConcurrentKeyRep{Key: "default-sdk", Value: "sdk-anchor"}, rep.SDKKeys[0])
	assert.Equal(t, ConcurrentKeyRep{Key: "service-a", Value: "sdk-service-a", Expiry: &expiryMs}, rep.SDKKeys[1])

	require.Len(t, rep.MobileKeys, 1)
	assert.Equal(t, ConcurrentKeyRep{Key: "mob-key-1", Value: "mob-f41c"}, rep.MobileKeys[0])

	params := rep.ToParams()

	require.Len(t, params.AcceptedSDKKeys, 2)
	assert.Equal(t, AcceptedSDKKey{Key:"default-sdk", Value: config.SDKKey("sdk-anchor")}, params.AcceptedSDKKeys[0])
	assert.Equal(t, AcceptedSDKKey{
		Key:"service-a",
		Value:      config.SDKKey("sdk-service-a"),
		Expiry:     time.UnixMilli(expiryMs),
	}, params.AcceptedSDKKeys[1])

	require.Len(t, params.AcceptedMobileKeys, 1)
	assert.Equal(t, AcceptedMobileKey{Key:"mob-key-1", Value: config.MobileKey("mob-f41c")}, params.AcceptedMobileKeys[0])
}

// TestEnvironmentRepOldFormatNoArrays verifies that an old-format payload (singular sdkKey/mobKey
// only, no sdkKeys/mobileKeys arrays) parses cleanly and leaves the accepted-set fields nil.
func TestEnvironmentRepOldFormatNoArrays(t *testing.T) {
	jsonStr := `{
		"envID": "envid1",
		"envKey": "envkey",
		"envName": "envname",
		"mobKey": "mob-default",
		"projKey": "projkey",
		"projName": "projname",
		"sdkKey": { "value": "sdk-key1" },
		"secureMode": false
	}`

	var rep EnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep))
	assert.Nil(t, rep.SDKKeys)
	assert.Nil(t, rep.MobileKeys)

	params := rep.ToParams()
	assert.Nil(t, params.AcceptedSDKKeys)
	assert.Nil(t, params.AcceptedMobileKeys)
	assert.Equal(t, config.SDKKey("sdk-key1"), params.SDKKey)
	assert.Equal(t, config.MobileKey("mob-default"), params.MobileKey)
}

// TestEnvironmentRepEmptyArrays verifies that a new-format payload carrying empty sdkKeys/mobileKeys
// arrays (not absent, but explicitly []) produces non-nil empty slices in AcceptedSDKKeys/
// AcceptedMobileKeys, preserving the nil/non-nil signal that distinguishes "old wire format" (nil)
// from "new format with no keys yet" (non-nil empty slice).
func TestEnvironmentRepEmptyArrays(t *testing.T) {
	jsonStr := `{
		"envID": "envid1",
		"sdkKey": { "value": "sdk-key1" },
		"mobKey": "mob-key1",
		"sdkKeys": [],
		"mobileKeys": []
	}`

	var rep EnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep))
	assert.NotNil(t, rep.SDKKeys, "explicitly-empty sdkKeys array must unmarshal to non-nil slice")
	assert.Len(t, rep.SDKKeys, 0)
	assert.NotNil(t, rep.MobileKeys, "explicitly-empty mobileKeys array must unmarshal to non-nil slice")
	assert.Len(t, rep.MobileKeys, 0)

	params := rep.ToParams()
	assert.NotNil(t, params.AcceptedSDKKeys, "non-nil empty sdkKeys must produce non-nil AcceptedSDKKeys")
	assert.Len(t, params.AcceptedSDKKeys, 0)
	assert.NotNil(t, params.AcceptedMobileKeys, "non-nil empty mobileKeys must produce non-nil AcceptedMobileKeys")
	assert.Len(t, params.AcceptedMobileKeys, 0)
}

// TestEnvironmentRepBackCompatOldRelay verifies that a new-format payload (containing sdkKeys/
// mobileKeys) is parsed cleanly by a relay that does not know about those fields. Go's JSON
// decoder ignores unknown fields by default; this test makes that guarantee explicit and durable.
// DisallowUnknownFields is intentionally not used in this parse path (see phase1-design.md §10).
func TestEnvironmentRepBackCompatOldRelay(t *testing.T) {
	// oldEnvironmentRep simulates a pre-concurrent-keys relay build — no SDKKeys/MobileKeys fields.
	type oldEnvironmentRep struct {
		EnvID   config.EnvironmentID `json:"envID"`
		MobKey  config.MobileKey     `json:"mobKey"`
		SDKKey  SDKKeyRep            `json:"sdkKey"`
		Version int                  `json:"version"`
	}

	jsonStr := `{
		"envID": "envid1",
		"envKey": "envkey",
		"envName": "envname",
		"mobKey": "mob-default",
		"projKey": "projkey",
		"projName": "projname",
		"sdkKey": { "value": "sdk-anchor" },
		"sdkKeys": [
			{ "key": "default-sdk", "value": "sdk-anchor" },
			{ "key": "service-a",   "value": "sdk-service-a" }
		],
		"mobileKeys": [
			{ "key": "mob-key-1", "value": "mob-default" }
		],
		"version": 5
	}`

	var rep oldEnvironmentRep
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &rep),
		"new-format payload must parse cleanly into a pre-concurrent-keys relay struct")
	assert.Equal(t, config.EnvironmentID("envid1"), rep.EnvID)
	assert.Equal(t, config.SDKKey("sdk-anchor"), rep.SDKKey.Value)
	assert.Equal(t, config.MobileKey("mob-default"), rep.MobKey)
	assert.Equal(t, 5, rep.Version)
}
