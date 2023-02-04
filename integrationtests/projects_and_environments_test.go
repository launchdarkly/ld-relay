// +build integrationtests

package integrationtests

import (
	"github.com/launchdarkly/ld-relay/v7/config"
)

type projectInfo struct {
	key  string
	name string
}

type environmentInfo struct {
	id        config.EnvironmentID
	key       string
	name      string
	sdkKey    config.SDKKey
	mobileKey config.MobileKey
	prefix    string
}

type projsAndEnvs map[projectInfo][]environmentInfo

func (pe projsAndEnvs) enumerateEnvs(fn func(projectInfo, environmentInfo)) {
	for proj, envs := range pe {
		for _, env := range envs {
			fn(proj, env)
		}
	}
}

func (pe projsAndEnvs) countEnvs() int {
	n := 0
	pe.enumerateEnvs(func(projectInfo, environmentInfo) { n++ })
	return n
}
