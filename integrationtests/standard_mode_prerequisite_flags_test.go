//go:build integrationtests

package integrationtests

import (
	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/stretchr/testify/require"
	"testing"
)

func withStandardModePrerequisitesTestData(t *testing.T, manager *integrationTestManager, fn func(data standardModeTestData, prereqs map[string][]string)) {
	project, envs, err := manager.apiHelper.createProject(1)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProject(project)

	flagKey := func(name string) string {
		return name + "-" + flagKeyForProj(project)
	}

	env := envs[0]
	toplevel1 := flagKey("toplevel1")
	prereq1 := flagKey("prereq1")
	prereq2 := flagKey("prereq2")

	err = manager.apiHelper.createFlagWithVariations(project, env, prereq1, true, ldvalue.Bool(false), ldvalue.Bool(true))
	require.NoError(t, err)

	err = manager.apiHelper.createFlagWithVariations(project, env, prereq2, true, ldvalue.Bool(false), ldvalue.Bool(true))
	require.NoError(t, err)

	prerequisites := map[string][]string{
		toplevel1: {prereq1, prereq2},
	}

	// The createFlagWithVariations call sets up two variations, with the second one being used if the flag is on.
	// The test here is to see which prerequisites were evaluated for a given flag. If a prerequisite fails, the eval
	// algorithm is going to short-circuit and we won't see the other prerequisite. So, we'll have two prerequisites,
	// both of which are on, and both of which are satisfied. That way the evaluator will be forced to visit both,
	// and we'll see the list of both when we query the eval endpoint.
	const onVariation = 1
	for flag, prereqs := range prerequisites {
		var ps []ldapi.Prerequisite
		for _, prereq := range prereqs {
			ps = append(ps, ldapi.Prerequisite{Key: prereq, Variation: onVariation})
		}
		err = manager.apiHelper.createFlagWithPrerequisites(project, env, flag, true, ldvalue.Bool(false), ldvalue.Bool(true), ps)
		require.NoError(t, err)
	}

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs{
			{key: project.key, name: project.name}: envs,
		},
	}

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
