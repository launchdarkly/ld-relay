//go:build integrationtests

package integrationtests

import (
	"github.com/launchdarkly/ld-relay/v9/config"
)

type projectInfo struct {
	key  string
	name string
}

type environmentInfo struct {
	id             config.EnvironmentID
	key            string
	name           string
	sdkKey         config.SDKKey
	expiringSdkKey config.SDKKey
	mobileKey      config.MobileKey
	prefix         string
	projKey        string
}

type projsAndEnvs map[projectInfo][]environmentInfo

func (pe projsAndEnvs) withoutExpiringKeys() projsAndEnvs {
	for _, envs := range pe {
		for i := range envs {
			envs[i].expiringSdkKey = ""
		}
	}
	return pe
}

func (pe projsAndEnvs) enumerateEnvs(fn func(projectInfo, environmentInfo)) {
	for proj, envs := range pe {
		for _, env := range envs {
			fn(proj, env)
		}
	}
}

func (pe projsAndEnvs) enumerateProjs(fn func(info projectInfo)) {
	for proj := range pe {
		fn(proj)
	}
}

func (pe projsAndEnvs) countEnvs() int {
	n := 0
	pe.enumerateEnvs(func(projectInfo, environmentInfo) { n++ })
	return n
}
