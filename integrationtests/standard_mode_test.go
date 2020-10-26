// +build integrationtests

package integrationtests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// See package_info.go for how these integration tests are configured and run.

type standardModeTestData struct {
	projsAndEnvs projsAndEnvs
}

func testStandardMode(t *testing.T, manager *integrationTestManager) {
	withStandardModeTestData(t, manager, func(testData standardModeTestData) {
		envVars := make(map[string]string)
		testData.projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			envVars["LD_ENV_"+string(env.name)] = string(env.sdkKey)
			envVars["LD_MOBILE_KEY_"+string(env.name)] = string(env.mobileKey)
			envVars["LD_CLIENT_SIDE_ID_"+string(env.name)] = string(env.id)
		})
		manager.startRelay(t, envVars)
		defer manager.stopRelay()

		manager.awaitEnvironments(t, testData.projsAndEnvs, false)
		manager.verifyFlagValues(t, testData.projsAndEnvs)
	})
}

func withStandardModeTestData(t *testing.T, manager *integrationTestManager, fn func(standardModeTestData)) {
	project1Info, environments1, err := manager.createProject(2)
	require.NoError(t, err)
	project2Info, environments2, err := manager.createProject(2)
	require.NoError(t, err)

	defer manager.deleteProject(project1Info)
	defer manager.deleteProject(project2Info)

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs{
			project1Info: environments1,
			project2Info: environments2,
		},
	}

	for proj, envs := range testData.projsAndEnvs {
		require.NoError(t, manager.createFlag(proj, envs, flagKeyForProj(proj), flagValueForEnv))
	}

	fn(testData)
}
