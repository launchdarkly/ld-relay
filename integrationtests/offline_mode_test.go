//go:build integrationtests

package integrationtests

// See package_info.go for how these integration tests are configured and run.

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/require"
)

type offlineModeTestData struct {
	projsAndEnvs  projsAndEnvs
	autoConfigKey config.AutoConfigKey
	autoConfigID  autoConfigID
}

// Used to configure the environment/project setup for an offline mode test.
type apiParams struct {
	// How many projects to create.
	numProjects int
	// Within each project, how many environments to create. Note: this must be >= 2 due to the way test flag
	// variations are setup.
	numEnvironments int
}

func testOfflineMode(t *testing.T, manager *integrationTestManager) {
	t.Run("expected environments and flag values", func(t *testing.T) {
		testExpectedEnvironmentsAndFlagValues(t, manager)
	})
	t.Run("sdk key is rotated with deprecation after relay has started", func(t *testing.T) {
		testSDKKeyRotatedAfterRelayStarted(t, manager)
	})
	t.Run("sdk key is rotated with deprecation before relay has started", func(t *testing.T) {
		testSDKKeyRotatedBeforeRelayStarted(t, manager)
	})
	t.Run("sdk key is rotated multiple times without deprecation after relay started", func(t *testing.T) {
		testSDKKeyRotatedWithoutDeprecation(t, manager)
	})
}

func testExpectedEnvironmentsAndFlagValues(t *testing.T, manager *integrationTestManager) {
	withOfflineModeTestData(t, manager, apiParams{numEnvironments: 2, numProjects: 2}, func(testData offlineModeTestData) {
		helpers.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE": filepath.Join(relayContainerSharedDir, fileName),
			})
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, testData.projsAndEnvs, &envPropertyExpectations{nameAndKey: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
			manager.verifyFlagValues(t, testData.projsAndEnvs)
		})
	})
}

func testSDKKeyRotatedAfterRelayStarted(t *testing.T, manager *integrationTestManager) {
	// If we download an archive with a primary SDK key, and then it is subsequently updated
	// with a deprecated key, we become initialized with both keys present.
	withOfflineModeTestData(t, manager, apiParams{numEnvironments: 2, numProjects: 1}, func(testData offlineModeTestData) {
		helpers.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE":                    filepath.Join(relayContainerSharedDir, fileName),
				"EXPIRED_CREDENTIAL_CLEANUP_INTERVAL": "100ms",
			})
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, testData.projsAndEnvs, &envPropertyExpectations{nameAndKey: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
			manager.verifyFlagValues(t, testData.projsAndEnvs)

			// The updated map will is modified to contain expiringSdkKey field (with the old SDK key) and
			// the new key set to whatever the API call returned.
			updated := manager.rotateSDKKeys(t, testData.projsAndEnvs, time.Now().Add(1*time.Hour))

			err = downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			// We are now asserting that the environment credentials returned by the status endpoint contains not just
			// the new SDK key, but the expiring one as well.
			manager.awaitEnvironments(t, updated, &envPropertyExpectations{nameAndKey: true, sdkKeys: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
		})
	})
}

func testSDKKeyRotatedBeforeRelayStarted(t *testing.T, manager *integrationTestManager) {
	// Upon startup if an archive contains a primary and deprecated key, we become initialized with both keys.
	withOfflineModeTestData(t, manager, apiParams{numEnvironments: 2, numProjects: 1}, func(testData offlineModeTestData) {
		helpers.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			// Rotation happens before starting up the relay.
			updated := manager.rotateSDKKeys(t, testData.projsAndEnvs, time.Now().Add(1*time.Hour))

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE":                    filepath.Join(relayContainerSharedDir, fileName),
				"EXPIRED_CREDENTIAL_CLEANUP_INTERVAL": "100ms",
			})
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, updated, &envPropertyExpectations{nameAndKey: true, sdkKeys: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
			manager.verifyFlagValues(t, testData.projsAndEnvs)
		})
	})
}

func testSDKKeyRotatedWithoutDeprecation(t *testing.T, manager *integrationTestManager) {

	// If a key is deprecated and then expires, it should be removed from the environment credentials.
	withOfflineModeTestData(t, manager, apiParams{numEnvironments: 2, numProjects: 1}, func(testData offlineModeTestData) {
		helpers.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			const keyGracePeriod = 5 * time.Second

			// Rotation happens before starting up the relay.
			updated := manager.rotateSDKKeys(t, testData.projsAndEnvs, time.Now().Add(keyGracePeriod))
			then := time.Now()

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE":                    filepath.Join(relayContainerSharedDir, fileName),
				"EXPIRED_CREDENTIAL_CLEANUP_INTERVAL": "100ms",
			})
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, updated, &envPropertyExpectations{nameAndKey: true, sdkKeys: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})

			// This test is timing-dependant on Relay removing the expired keys before we check the /status endpoint.
			// To keep the test fast, only sleep as long as necessary to ensure the keys have expired.
			toSleep := keyGracePeriod - time.Since(then)
			if toSleep > 0 {
				time.Sleep(toSleep)
			}
			manager.awaitEnvironments(t, updated.withoutExpiringKeys(), &envPropertyExpectations{nameAndKey: true, sdkKeys: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
		})
	})
}

func testKeyIsRotatedWithoutGracePeriod(t *testing.T, manager *integrationTestManager) {

	// If a key is rotated without a grace period, then the old one should be revoked immediately.
	// If a key is deprecated and then expires, it should be removed from the environment credentials.
	withOfflineModeTestData(t, manager, apiParams{numEnvironments: 2, numProjects: 1}, func(testData offlineModeTestData) {
		helpers.WithTempDir(func(dirPath string) {
			fileName := "archive.tar.gz"
			filePath := filepath.Join(manager.relaySharedDir, fileName)

			err := downloadRelayArchive(manager, testData.autoConfigKey, filePath)
			manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
			require.NoError(t, err)

			// Relay will check for expired keys at this interval.
			cleanupInterval := 100 * time.Millisecond
			// We'll sleep longer than the interval after rotating keys, to try and reduce test flakiness.
			cleanupIntervalBuffer := 1 * time.Second

			fmt.Println(cleanupInterval)
			manager.startRelay(t, map[string]string{
				"FILE_DATA_SOURCE":                    filepath.Join(relayContainerSharedDir, fileName),
				"EXPIRED_CREDENTIAL_CLEANUP_INTERVAL": cleanupInterval.String(),
			})
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, testData.projsAndEnvs, &envPropertyExpectations{nameAndKey: true}, func(proj projectInfo, env environmentInfo) string {
				return string(env.id)
			})
			manager.verifyFlagValues(t, testData.projsAndEnvs)

			updated := maps.Clone(testData.projsAndEnvs)

			// Check that the rotation logic holds for more than one rotation.
			const numRotations = 3
			for i := 0; i < numRotations; i++ {
				// time.Time{} to signify that there's no deprecation period.
				updated = manager.rotateSDKKeys(t, updated, time.Time{})

				err = downloadRelayArchive(manager, testData.autoConfigKey, filePath)
				manager.apiHelper.logResult("Download data archive from /relay/latest-all to "+filePath, err)
				require.NoError(t, err)

				time.Sleep(cleanupIntervalBuffer)

				// We are now asserting that the SDK key was rotated (and that there's no expiringSDKKey).
				manager.awaitEnvironments(t, updated, &envPropertyExpectations{nameAndKey: true, sdkKeys: true}, func(proj projectInfo, env environmentInfo) string {
					return string(env.id)
				})
			}
		})
	})
}

func withOfflineModeTestData(t *testing.T, manager *integrationTestManager, cfg apiParams, fn func(offlineModeTestData)) {
	projsAndEnvs, err := manager.apiHelper.createProjectsAndEnvironments(cfg.numProjects, cfg.numEnvironments)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProjects(projsAndEnvs)

	policyResources := []string{}
	for p := range projsAndEnvs {
		policyResources = append(policyResources, fmt.Sprintf("proj/%s:env/*", p.key))
	}
	configID, configKey, err := manager.apiHelper.createAutoConfigKey(policyResources)
	require.NoError(t, err)

	defer manager.apiHelper.deleteAutoConfigKey(configID)

	require.NoError(t, manager.apiHelper.createFlags(projsAndEnvs))

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
	// if we succeeded
	if err != nil {
		return err
	}
	manager.requestLogger.logResponse(resp, resp.StatusCode != 200)
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
