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
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, testData.projsAndEnvs, false, func(proj projectInfo, env environmentInfo) string {
			return string(env.name)
		})
		manager.verifyFlagValues(t, testData.projsAndEnvs)
	})
}

func withStandardModeTestData(t *testing.T, manager *integrationTestManager, fn func(standardModeTestData)) {
	projsAndEnvs, err := manager.apiHelper.createProjectsAndEnvironments(2, 2)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProjects(projsAndEnvs)

	require.NoError(t, manager.apiHelper.createFlags(projsAndEnvs))

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs,
	}
	fn(testData)
}
