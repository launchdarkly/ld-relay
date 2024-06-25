//go:build integrationtests

package integrationtests

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withStandardModePayloadFiltersTestData(t *testing.T, manager *integrationTestManager, fn func(data standardModeTestData)) {
	projsAndEnvs, err := manager.apiHelper.createProjectsAndEnvironmentsWithFilters(2, 2, 2)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProjects(projsAndEnvs)

	require.NoError(t, manager.apiHelper.createFlags(projsAndEnvs))

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs,
	}
	// This is here because the backend takes a while to setup the filters, otherwise we get 404s when connecting.
	time.Sleep(10 * time.Second)
	fn(testData)
}

func testStandardModeWithDefaultFilters(t *testing.T, manager *integrationTestManager) {
	withStandardModePayloadFiltersTestData(t, manager, func(testData standardModeTestData) {
		envVars := make(map[string]string)
		testData.projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			envVars["LD_ENV_"+env.name] = string(env.sdkKey)
			envVars["LD_MOBILE_KEY_"+env.name] = string(env.mobileKey)
			envVars["LD_CLIENT_SIDE_ID_"+env.name] = string(env.id)
			envVars["LD_PROJ_KEY_"+env.name] = env.projKey
		})

		testData.projsAndEnvs.enumerateProjs(func(info projectInfo) {
			envVars["LD_FILTER_KEYS_"+info.key] = info.filters
		})

		manager.startRelay(t, envVars)
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, testData.projsAndEnvs, nil, func(proj projectInfo, env environmentInfo) string {
			if env.filterKey == "" {
				return env.key
			}
			return fmt.Sprintf("%s/%s", env.key, env.filterKey)
		})
		manager.verifyFlagValues(t, testData.projsAndEnvs)
	})
}

func withStandardModeSpecificPayloadFiltersTestData(t *testing.T, manager *integrationTestManager, fn func(data standardModeTestData)) {
	projsAndEnvs, err := manager.apiHelper.createProjectsAndEnvironmentsWithSpecificFilters(2, 2, map[string]filterRules{
		"even-flags": {
			filterRule{
				action: "include",
				condition: filterCondition{
					kind:     "string-match",
					property: "flag-key",
					regex:    "flag[0246].*",
				},
			},
		},
		"odd-flags": {
			filterRule{
				action: "include",
				condition: filterCondition{
					kind:     "string-match",
					property: "flag-key",
					regex:    "flag[1357].*",
				},
			},
		},
	})
	require.NoError(t, err)
	defer manager.apiHelper.deleteProjects(projsAndEnvs)

	require.NoError(t, manager.apiHelper.createEvenAndOddFlags(projsAndEnvs))

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs,
	}
	// This is here because the backend takes a while to setup the filters, otherwise we get 404s when connecting.
	time.Sleep(10 * time.Second)
	fn(testData)
}

func testStandardModeWithSpecificFilters(t *testing.T, manager *integrationTestManager) {
	withStandardModeSpecificPayloadFiltersTestData(t, manager, func(testData standardModeTestData) {
		envVars := make(map[string]string)
		testData.projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			envVars["LD_ENV_"+env.name] = string(env.sdkKey)
			envVars["LD_MOBILE_KEY_"+env.name] = string(env.mobileKey)
			envVars["LD_CLIENT_SIDE_ID_"+env.name] = string(env.id)
			envVars["LD_PROJ_KEY_"+env.name] = env.projKey
		})

		testData.projsAndEnvs.enumerateProjs(func(info projectInfo) {
			envVars["LD_FILTER_KEYS_"+info.key] = info.filters
		})

		manager.startRelay(t, envVars)
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, testData.projsAndEnvs, nil, func(proj projectInfo, env environmentInfo) string {
			if env.filterKey == "" {
				return env.key
			}
			return fmt.Sprintf("%s/%s", env.key, env.filterKey)
		})
		manager.verifyEvenOddFlagKeys(t, testData.projsAndEnvs)
	})
}
