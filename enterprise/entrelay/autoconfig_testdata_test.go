package entrelay

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	c "github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

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

func (e testAutoConfEnv) json() string {
	sdkKey := map[string]interface{}{"value": e.sdkKey}
	if e.sdkKeyExpiryValue != "" {
		sdkKey["expiring"] = map[string]interface{}{"value": e.sdkKeyExpiryValue, "timestamp": e.sdkKeyExpiryTime}
	}
	props := map[string]interface{}{
		"envId":    e.id,
		"envKey":   e.envKey,
		"envName":  e.envName,
		"mobKey":   e.mobKey,
		"projKey":  e.projKey,
		"projName": e.projName,
		"sdkKey":   sdkKey,
		"version":  e.version,
	}
	bytes, _ := json.Marshal(props)
	return string(bytes)
}

func makeAutoConfPutEvent(envs ...testAutoConfEnv) httphelpers.SSEEvent {
	m := make(map[string]interface{})
	for _, e := range envs {
		m[string(e.id)] = json.RawMessage(e.json())
	}
	mj, _ := json.Marshal(m)
	data := fmt.Sprintf(`{"path":"/","data":{"environments":%s}}`, string(mj))
	return httphelpers.SSEEvent{Event: "put", Data: strings.ReplaceAll(data, "\n", "")}
}

func makeAutoConfPatchEvent(env testAutoConfEnv) httphelpers.SSEEvent {
	data := fmt.Sprintf(`{"path":"/environments/%s","data":%s}`, env.id, env.json())
	return httphelpers.SSEEvent{Event: "patch", Data: strings.ReplaceAll(data, "\n", "")}
}

func makeAutoConfDeleteEvent(envID c.EnvironmentID, version int) httphelpers.SSEEvent {
	data := fmt.Sprintf(`{"path":"/environments/%s", "version":%d}`, envID, version)
	return httphelpers.SSEEvent{Event: "delete", Data: data}
}
