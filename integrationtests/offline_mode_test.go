// +build integrationtests

package integrationtests

// See package_info.go for how these integration tests are configured and run.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/stretchr/testify/require"
)

type offlineModeTestData struct {
	projsAndEnvs  projsAndEnvs
	autoConfigKey config.AutoConfigKey
	autoConfigID  autoConfigID
}

func testOfflineMode(t *testing.T, manager *integrationTestManager) {
	withOfflineModeTestData(t, manager, func(testData offlineModeTestData) {
		sharedtest.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE": filepath.Join(relayContainerSharedDir, fileName),
			})
			defer manager.stopRelay()

			manager.awaitEnvironments(t, testData.projsAndEnvs, true, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
			manager.verifyFlagValues(t, testData.projsAndEnvs)
		})
	})
}

func withOfflineModeTestData(t *testing.T, manager *integrationTestManager, fn func(offlineModeTestData)) {
	projsAndEnvs, err := manager.createProjectsAndEnvironments(2, 2)
	require.NoError(t, err)
	defer manager.deleteProjects(projsAndEnvs)

	policyResources := []string{}
	for p := range projsAndEnvs {
		policyResources = append(policyResources, fmt.Sprintf("proj/%s:env/*", p.key))
	}
	configID, configKey, err := manager.createAutoConfigKey(policyResources)
	require.NoError(t, err)

	defer manager.deleteAutoConfigKey(configID)

	require.NoError(t, manager.createFlags(projsAndEnvs))

	testData := offlineModeTestData{
		projsAndEnvs:  projsAndEnvs,
		autoConfigKey: configKey,
		autoConfigID:  configID,
	}

	fn(testData)
}

func downloadRelayArchive(manager *integrationTestManager, configKey config.AutoConfigKey, filePath string) error {
	url := manager.sdkURL + "/relay/latest-all"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", string(configKey))
	manager.requestLogger.logRequest(req)
	resp, err := http.DefaultClient.Do(req)
	// using default client instead of manager.httpClient because, if HTTP logging is enabled, we do *not* want the response
	// body (a gzip file) to be written to the log - so we call logResponse ourselves below, with false for the 2nd parameter
	if err != nil {
		return err
	}
	manager.requestLogger.logResponse(resp, false)
	if resp.StatusCode != 200 {
		return fmt.Errorf("response status %d", resp.StatusCode)
	}

	defer resp.Body.Close()
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, resp.Body)
	return err
}
