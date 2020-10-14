package relay

import (
	"encoding/json"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

const testAutoConfKey = c.AutoConfigKey("test-auto-conf-key")

var testAutoConfDefaultConfig = c.Config{
	AutoConfig: c.AutoConfigConfig{Key: testAutoConfKey},
}

type testAutoConfEnv struct {
	id                c.EnvironmentID
	envKey            string
	envName           string
	mobKey            c.MobileKey
	projKey           string
	projName          string
	sdkKey            c.SDKKey
	sdkKeyExpiryValue c.SDKKey
	sdkKeyExpiryTime  ldtime.UnixMillisecondTime
	version           int
}

var (
	testAutoConfEnv1 = testAutoConfEnv{
		id:       c.EnvironmentID("envid1"),
		envKey:   "envkey1",
		envName:  "envname1",
		mobKey:   c.MobileKey("mobkey1"),
		projKey:  "projkey1",
		projName: "projname1",
		sdkKey:   c.SDKKey("sdkkey1"),
		version:  10,
	}

	testAutoConfEnv2 = testAutoConfEnv{
		id:       c.EnvironmentID("envid2"),
		envKey:   "envkey2",
		envName:  "envname2",
		mobKey:   c.MobileKey("mobkey2"),
		projKey:  "projkey2",
		projName: "projname2",
		sdkKey:   c.SDKKey("sdkkey2"),
		version:  11,
	}
)

func (e testAutoConfEnv) toEnvironmentRep() envfactory.EnvironmentRep {
	rep := envfactory.EnvironmentRep{
		EnvID:    e.id,
		EnvKey:   e.envKey,
		EnvName:  e.envName,
		MobKey:   e.mobKey,
		ProjKey:  e.projKey,
		ProjName: e.projName,
		SDKKey: envfactory.SDKKeyRep{
			Value: e.sdkKey,
		},
		Version: e.version,
	}
	if e.sdkKeyExpiryValue != "" {
		rep.SDKKey.Expiring.Value = e.sdkKeyExpiryValue
		rep.SDKKey.Expiring.Timestamp = e.sdkKeyExpiryTime
	}
	return rep
}

func (e testAutoConfEnv) params() envfactory.EnvironmentParams {
	return e.toEnvironmentRep().ToParams()
}

func makeAutoConfPutEvent(envs ...testAutoConfEnv) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{Path: "/", Data: autoconfig.PutContent{
		Environments: make(map[c.EnvironmentID]envfactory.EnvironmentRep)}}
	for _, e := range envs {
		data.Data.Environments[e.id] = e.toEnvironmentRep()
	}
	jsonData, _ := json.Marshal(data)
	return httphelpers.SSEEvent{Event: autoconfig.PutEvent, Data: string(jsonData)}
}

func makeAutoConfPatchEvent(env testAutoConfEnv) httphelpers.SSEEvent {
	jsonData, _ := json.Marshal(autoconfig.PatchMessageData{Path: "/environments/" + string(env.id),
		Data: env.toEnvironmentRep()})
	return httphelpers.SSEEvent{Event: autoconfig.PatchEvent, Data: string(jsonData)}
}

func makeAutoConfDeleteEvent(envID c.EnvironmentID, version int) httphelpers.SSEEvent {
	jsonData, _ := json.Marshal(autoconfig.DeleteMessageData{Path: "/environments/" + string(envID), Version: version})
	return httphelpers.SSEEvent{Event: autoconfig.DeleteEvent, Data: string(jsonData)}
}
