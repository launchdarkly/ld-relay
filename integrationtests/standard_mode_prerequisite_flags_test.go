//go:build integrationtests

package integrationtests

import (
	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func withStandardModePrerequisitesTestData(t *testing.T, manager *integrationTestManager, fn func(data standardModeTestData, prereqs map[string][]string)) {
	project, envs, err := manager.apiHelper.createProject(1)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProject(project)

	trueVal := func(info environmentInfo) ldvalue.Value {
		return ldvalue.Bool(true)
	}

	err = manager.apiHelper.createFlag(project, envs, "prereq1", trueVal)
	require.NoError(t, err)

	err = manager.apiHelper.createFlag(project, envs, "prereq2", trueVal)
	require.NoError(t, err)

	prerequisites := map[string][]string{
		"flag1": {"prereq1", "prereq2"},
	}

	for flag, prereqs := range prerequisites {
		var ps []ldapi.Prerequisite
		for _, prereq := range prereqs {
			ps = append(ps, ldapi.Prerequisite{Key: prereq, Variation: 0})
		}
		err = manager.apiHelper.createFlagWithPrerequisites(project, envs[0], flag, ldvalue.Bool(true), ps)
		require.NoError(t, err)
	}

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs{
			{key: project.key, name: project.name}: envs,
		},
	}

	// This is here because the backend takes a while to setup the filters, otherwise we get 404s when connecting.
	time.Sleep(10 * time.Second)
	fn(testData, prerequisites)
}

func testStandardModeWithPrerequisites(t *testing.T, manager *integrationTestManager) {
	withStandardModePrerequisitesTestData(t, manager, func(testData standardModeTestData, prerequisites map[string][]string) {
		envVars := make(map[string]string)
		testData.projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			envVars["LD_ENV_"+string(env.name)] = string(env.sdkKey)
			envVars["LD_MOBILE_KEY_"+string(env.name)] = string(env.mobileKey)
			envVars["LD_CLIENT_SIDE_ID_"+string(env.name)] = string(env.id)
		})
		manager.startRelay(t, envVars)
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, testData.projsAndEnvs, nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})
		manager.verifyFlagPrerequisites(t, testData.projsAndEnvs, prerequisites)
	})
}
