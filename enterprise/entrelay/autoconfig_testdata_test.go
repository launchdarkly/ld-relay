package entrelay

import (
	"encoding/json"
	"testing"

	c "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay-core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/enterprise/autoconfig"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
)

const testAutoConfKey = entconfig.AutoConfigKey("test-auto-conf-key")

var testAutoConfDefaultConfig = entconfig.EnterpriseConfig{
	AutoConfig: entconfig.AutoConfigConfig{Key: testAutoConfKey},
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

func (e testAutoConfEnv) toEnvironmentRep() autoconfig.EnvironmentRep {
	rep := autoconfig.EnvironmentRep{
		EnvID:    e.id,
		EnvKey:   e.envKey,
		EnvName:  e.envName,
		MobKey:   e.mobKey,
		ProjKey:  e.projKey,
		ProjName: e.projName,
		SDKKey: autoconfig.SDKKeyRep{
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

func makeAutoConfPutEvent(envs ...testAutoConfEnv) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{Path: "/", Data: autoconfig.PutContent{
		Environments: make(map[c.EnvironmentID]autoconfig.EnvironmentRep)}}
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

func assertEnvProps(t *testing.T, expected testAutoConfEnv, env relayenv.EnvContext) {
	assert.Equal(t, credentialsAsSet(expected.id, expected.mobKey, expected.sdkKey), credentialsAsSet(env.GetCredentials()...))
	assert.Equal(t, relayenv.EnvIdentifiers{
		EnvKey:   expected.envKey,
		EnvName:  expected.envName,
		ProjKey:  expected.projKey,
		ProjName: expected.projName,
	}, env.GetIdentifiers())
	assert.Equal(t, expected.projName+" "+expected.envName, env.GetIdentifiers().GetDisplayName())
}

func credentialsAsSet(cs ...c.SDKCredential) map[c.SDKCredential]struct{} {
	ret := make(map[c.SDKCredential]struct{}, len(cs))
	for _, c := range cs {
		ret[c] = struct{}{}
	}
	return ret
}
