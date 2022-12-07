package envfactory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"

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
		SDKKey:         env2.SDKKey.Value,
		ExpiringSDKKey: env2.SDKKey.Expiring.Value,
		MobileKey:      env2.MobKey,
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
