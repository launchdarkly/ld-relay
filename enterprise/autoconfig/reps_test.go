package autoconfig

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

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
