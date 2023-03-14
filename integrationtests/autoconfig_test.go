//go:build integrationtests
// +build integrationtests

package integrationtests

// See package_info.go for how these integration tests are configured and run.

import (
	"fmt"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

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

	t.Run("updated SDK key", func(t *testing.T) {
		testUpdatedSDKKeyWithoutExpiry(t, manager)
	})

	t.Run("updated SDK key with expiry", func(t *testing.T) {
		testUpdatedSDKKeyWithExpiry(t, manager)
	})

	t.Run("updated SDK key with expiry before starting Relay", func(t *testing.T) {
		testUpdatedSDKKeyWithExpiryBeforeStartingRelay(t, manager)
	})

	t.Run("updated mobile key", func(t *testing.T) {
		testUpdatedMobileKey(t, manager)
	})
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
		err := manager.apiHelper.updateAutoConfigPolicy(testData.autoConfigID, newPolicyResources)
		require.NoError(t, err)

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == 1 {
				if envStatus, ok := status.Environments[string(remainingEnv.id)]; ok {
					verifyEnvProperties(t, testData.project, remainingEnv, envStatus, true)
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

		newEnv, err := manager.apiHelper.addEnvironment(testData.project)
		require.NoError(t, err)

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == len(testData.environments)+1 {
				if envStatus, ok := status.Environments[string(newEnv.id)]; ok {
					verifyEnvProperties(t, testData.project, newEnv, envStatus, true)
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

		err := manager.apiHelper.deleteEnvironment(testData.project, envToDelete)
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

func testUpdatedSDKKeyWithoutExpiry(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		envToUpdate := testData.environments[0]

		newKey, err := manager.apiHelper.rotateSDKKey(testData.project, envToUpdate, time.Time{})
		require.NoError(t, err)

		updatedEnv := envToUpdate
		updatedEnv.sdkKey = newKey

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if envStatus, ok := status.Environments[string(envToUpdate.id)]; ok {
				verifyEnvProperties(t, testData.project, updatedEnv, envStatus, true)
				return last5(envStatus.SDKKey) == last5(string(newKey)) && envStatus.ExpiringSDKKey == ""
			}
			return false
		})
	})
}

func testUpdatedSDKKeyWithExpiry(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		envToUpdate := testData.environments[0]
		oldKey := envToUpdate.sdkKey

		projAndEnvs := projsAndEnvs{testData.project: testData.environments}
		require.NoError(t, manager.apiHelper.createFlags(projAndEnvs))

		newKey, err := manager.apiHelper.rotateSDKKey(testData.project, envToUpdate, time.Now().Add(time.Hour))
		require.NoError(t, err)

		updatedEnv := envToUpdate
		updatedEnv.sdkKey = newKey

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			// verifyFlagValues can't succeed if Relay isn't actually ready to serve traffic,
			// so bail out and try again here.
			if status.Status != "healthy" {
				return false
			}
			if envStatus, ok := status.Environments[string(envToUpdate.id)]; ok {
				verifyEnvProperties(t, testData.project, updatedEnv, envStatus, true)
				return last5(envStatus.SDKKey) == last5(string(newKey)) &&
					last5(envStatus.ExpiringSDKKey) == last5(string(oldKey))
			}
			return false
		})

		// Poll for flags from both of the environments - the SDK key in the first environment here is the
		// original one, which should still work since it has not yet expired
		manager.verifyFlagValues(t, projAndEnvs)

		// And poll for flags with the new SDK key, which should also work
		manager.verifyFlagValues(t, projsAndEnvs{testData.project: []environmentInfo{updatedEnv}})
	})
}

func testUpdatedSDKKeyWithExpiryBeforeStartingRelay(t *testing.T, manager *integrationTestManager) {
	testData := setupAutoConfigTestData(t, manager)
	defer manager.apiHelper.deleteProject(testData.project)
	defer manager.apiHelper.deleteAutoConfigKey(testData.autoConfigID)

	projAndEnvs := projsAndEnvs{testData.project: testData.environments}
	require.NoError(t, manager.apiHelper.createFlags(projAndEnvs))

	envToUpdate := testData.environments[0]
	oldKey := envToUpdate.sdkKey

	newKey, err := manager.apiHelper.rotateSDKKey(testData.project, envToUpdate, time.Now().Add(time.Hour))
	require.NoError(t, err)

	updatedEnv := envToUpdate
	updatedEnv.sdkKey = newKey

	manager.startRelay(t, map[string]string{
		"AUTO_CONFIG_KEY": string(testData.autoConfigKey),
	})
	defer manager.stopRelay(t)

	manager.awaitEnvironments(t, projAndEnvs, false, func(proj projectInfo, env environmentInfo) string {
		return string(env.id)
	})

	manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
		if envStatus, ok := status.Environments[string(envToUpdate.id)]; ok {
			verifyEnvProperties(t, testData.project, updatedEnv, envStatus, true)
			return last5(envStatus.SDKKey) == last5(string(newKey)) &&
				last5(envStatus.ExpiringSDKKey) == last5(string(oldKey))
		}
		return false
	})

	// Poll for flags from both of the environments - the SDK key in the first environment here is the
	// original one, which should still work since it has not yet expired
	manager.verifyFlagValues(t, projAndEnvs)

	// And poll for flags with the new SDK key, which should also work
	manager.verifyFlagValues(t, projsAndEnvs{testData.project: []environmentInfo{updatedEnv}})
}

func testUpdatedMobileKey(t *testing.T, manager *integrationTestManager) {
	withRelayAndTestData(t, manager, func(testData autoConfigTestData) {
		awaitInitialState(t, manager, testData)
		envToUpdate := testData.environments[0]

		newKey, err := manager.apiHelper.rotateMobileKey(testData.project, envToUpdate)
		require.NoError(t, err)

		updatedEnv := envToUpdate
		updatedEnv.mobileKey = newKey

		manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if envStatus, ok := status.Environments[string(envToUpdate.id)]; ok {
				verifyEnvProperties(t, testData.project, updatedEnv, envStatus, true)
				return last5(envStatus.MobileKey) == last5(string(newKey))
			}
			return false
		})
	})
}

func last5(s string) string {
	if len(s) < 5 {
		return s
	}
	return s[len(s)-5:]
}

func setupAutoConfigTestData(t *testing.T, manager *integrationTestManager) autoConfigTestData {
	projectInfo, environments, err := manager.apiHelper.createProject(2)
	require.NoError(t, err)

	policyResources := []string{
		fmt.Sprintf("proj/%s:env/*", projectInfo.key),
	}
	configID, configKey, err := manager.apiHelper.createAutoConfigKey(policyResources)
	require.NoError(t, err)

	return autoConfigTestData{
		project:       projectInfo,
		environments:  environments,
		autoConfigKey: configKey,
		autoConfigID:  configID,
	}
}

func withRelayAndTestData(t *testing.T, manager *integrationTestManager, action func(autoConfigTestData)) {
	testData := setupAutoConfigTestData(t, manager)
	defer manager.apiHelper.deleteProject(testData.project)
	defer manager.apiHelper.deleteAutoConfigKey(testData.autoConfigID)

	manager.startRelay(t, map[string]string{
		"AUTO_CONFIG_KEY": string(testData.autoConfigKey),
	})
	defer manager.stopRelay(t)

	action(testData)
}

func awaitInitialState(t *testing.T, manager *integrationTestManager, testData autoConfigTestData) {
	projsAndEnvs := projsAndEnvs{testData.project: testData.environments}
	manager.awaitEnvironments(t, projsAndEnvs, true, func(proj projectInfo, env environmentInfo) string {
		return string(env.id)
	})
}
