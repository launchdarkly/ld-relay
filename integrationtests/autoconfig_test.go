// +build integrationtests

package integrationtests

// See package_info.go for how these integration tests are configured and run.

import (
	"fmt"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type autoConfigTestData struct {
	project       projectInfo
	environments  []environmentInfo
	autoConfigKey config.AutoConfigKey
	autoConfigID  autoConfigID
}

func testAutoConfig(t *testing.T, manager *integrationTestManager) {
	t.Run("initial environment list", func(t *testing.T) {
		testInitialEnvironmentList(t, manager)
	})

	t.Run("policy update", func(t *testing.T) {
		testPolicyUpdate(t, manager)
	})

	t.Run("add environment", func(t *testing.T) {
		testAddEnvironment(t, manager)
	})

	t.Run("delete environment", func(t *testing.T) {
		testDeleteEnvironment(t, manager)
	})

	// this test is currently disabled because we don't yet have an API endpoint for rotating an SDK key
	// t.Run("expiring SDK key", func(t *testing.T) {
	// 	testExpiringSDKKey(t, manager)
	// })
}

func testInitialEnvironmentList(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
	})
}

func testPolicyUpdate(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		remainingEnv := testData.environments[1]

		// Change the policy to exclude the first environment
		newPolicyResources := []string{
			fmt.Sprintf("proj/%s:env/%s", testData.project.key, testData.environments[1].key),
		}
		err := manager.updateAutoConfigPolicy(testData.autoConfigID, newPolicyResources)
		require.NoError(t, err)

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == 1 {
				if envStatus, ok := status.Environments[string(remainingEnv.id)]; ok {
					verifyEnvProperties(t, testData.project, remainingEnv, envStatus)
					return true
				}
			}
			return false
		})
	})
}

func testAddEnvironment(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)

		newEnv, err := manager.addEnvironment(testData.project)
		require.NoError(t, err)

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == len(testData.environments)+1 {
				if envStatus, ok := status.Environments[string(newEnv.id)]; ok {
					verifyEnvProperties(t, testData.project, newEnv, envStatus)
					return true
				}
			}
			return false
		})
	})
}

func testDeleteEnvironment(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		envToDelete := testData.environments[0]

		err := manager.deleteEnvironment(testData.project, envToDelete)
		require.NoError(t, err)

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == len(testData.environments)-1 {
				_, found := status.Environments[string(envToDelete.id)]
				return !found
			}
			return false
		})
	})
}

func testExpiringSDKKey(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		envToUpdate := testData.environments[0]
		oldKey := envToUpdate.sdkKey

		newKey, err := manager.rotateSDKKey(testData.project, envToUpdate, time.Hour)
		require.NoError(t, err)

		updatedEnv := envToUpdate
		updatedEnv.sdkKey = newKey

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if envStatus, ok := status.Environments[string(envToUpdate.id)]; ok {
				verifyEnvProperties(t, testData.project, updatedEnv, envStatus)
				return envStatus.ExpiringSDKKey == string(oldKey)
			}
			return false
		})
	})
}

func setupTestData(t *testing.T, manager *integrationTestManager) autoConfigTestData {
	projectInfo, environments, err := manager.createProject(2)
	require.NoError(t, err)

	policyResources := []string{
		fmt.Sprintf("proj/%s:env/*", projectInfo.key),
	}
	configID, configKey, err := manager.createAutoConfigKey(policyResources)
	require.NoError(t, err)

	return autoConfigTestData{
		project:       projectInfo,
		environments:  environments,
		autoConfigKey: configKey,
		autoConfigID:  configID,
	}
}

func withRelayAndTestData(t *testing.T, manager *integrationTestManager, action func(autoConfigTestData)) {
	testData := setupTestData(t, manager)
	defer manager.deleteProject(testData.project)
	defer manager.deleteAutoConfigKey(testData.autoConfigID)

	manager.startRelay(t, map[string]string{
		"AUTO_CONFIG_KEY": string(testData.autoConfigKey),
	})
	defer manager.stopRelay()

	action(testData)
}

func awaitInitialState(t *testing.T, manager *integrationTestManager, testData autoConfigTestData) {
	_, success := manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
		if len(status.Environments) == len(testData.environments) {
			for _, e := range testData.environments {
				if envStatus, ok := status.Environments[string(e.id)]; ok {
					verifyEnvProperties(t, testData.project, e, envStatus)
					if envStatus.Status != "connected" {
						return false
					}
				} else {
					return false
				}
			}
			return true
		}
		return false
	})
	if !success {
		t.FailNow()
	}
}

func verifyEnvProperties(t *testing.T, project projectInfo, environment environmentInfo, envStatus core.EnvironmentStatusRep) {
	assert.Equal(t, string(environment.id), envStatus.EnvID)
	assert.Equal(t, environment.name, envStatus.EnvName)
	assert.Equal(t, environment.key, envStatus.EnvKey)
	assert.Equal(t, project.name, envStatus.ProjName)
	assert.Equal(t, project.key, envStatus.ProjKey)
}
