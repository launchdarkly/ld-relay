//go:build integrationtests

package integrationtests

import (
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"
)

type projectInfo struct {
	key     string
	name    string
	filters string
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

	// this is a synthetic field, set only when this environment is a filtered environment.
	filterKey config.FilterKey
}

type projsAndEnvs map[projectInfo][]environmentInfo

func (pe projsAndEnvs) enumerateEnvs(fn func(projectInfo, environmentInfo)) {
	for proj, envs := range pe {
		for _, env := range envs {
			fn(proj, env)
		}
		if proj.filters == "" {
			continue
		}
		for _, filter := range strings.Split(proj.filters, ",") {
			for _, env := range envs {
				filteredEnv := env
				filteredEnv.filterKey = config.FilterKey(filter)
				fn(proj, filteredEnv)
			}
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
