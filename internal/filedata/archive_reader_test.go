package filedata

import (
	"os"
	"sort"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"

	helpers "github.com/launchdarkly/go-test-helpers/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCompressedArchive(t *testing.T) {
	// ArchiveReader is able to read either .tar or .tar.gz files, which it auto-detects. This test verifies
	// that it can handle a .tar.gz file. All of the other tests use just a .tar file to avoid the overhead
	// of compressing and uncompressing.

	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, true, nil, allTestEnvs...)
		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)
		defer ar.Close()

		verifyAllEnvironmentData(t, ar)
	})
}

func TestReadUncompressedArchive(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, nil, allTestEnvs...)

		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)
		defer ar.Close()

		verifyAllEnvironmentData(t, ar)
	})
}

func TestReadArchiveWithNoEnvironments(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, nil)

		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)
		defer ar.Close()

		assert.Len(t, ar.GetEnvironmentIDs(), 0)
	})
}

func TestErrorOnFileNotFound(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		require.NoError(t, os.Remove(filePath))

		_, err := newArchiveReader(filePath)
		require.Error(t, err)
	})
}

func TestErrorOnMissingChecksumFile(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, removeChecksumFileFromArchive, allTestEnvs...)

		_, err := newArchiveReader(filePath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
		assert.Contains(t, err.Error(), environmentsChecksumFileName)
	})
}

func TestErrorOnBadChecksum(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, makeChecksumFileInvalidInArchive, allTestEnvs...)
		_, err := newArchiveReader(filePath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checksum of environments did not match")
	})
}

func TestEnvironmentHasMalformedMetadata(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, func(dirPath string) {
			badData := []byte("whatever")
			require.NoError(t, os.WriteFile(envMetadataFilePath(dirPath, testEnv1.id()), badData, 0600))
			rehash(dirPath, testEnv1.id())
		}, testEnv1)
		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)

		_, err = ar.GetEnvironmentMetadata(testEnv1.id())
		require.Error(t, err)
	})
}

func TestEnvironmentHasMalformedSDKDataItem(t *testing.T) {
	te := testEnv1
	te.sdkData = map[string]map[string]interface{}{
		"flags": map[string]interface{}{
			"env1Flag1": testEnv1.sdkData["flags"]["env1Flag1"],
			"badFlag": map[string]interface{}{
				"key": 3,
			},
		},
	}

	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, nil, te)
		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)

		_, err = ar.GetEnvironmentSDKData(te.id())
		require.Error(t, err)
		assert.Equal(t, errBadItemJSON("badFlag", "flags"), err)
	})
}

func TestEnvironmentSDKDataItemOfUnknownKindIsIgnored(t *testing.T) {
	te := testEnv1
	te.sdkData = map[string]map[string]interface{}{
		"flags": testEnv1.sdkData["flags"],
		"cats": map[string]interface{}{
			"Lucy": map[string]interface{}{},
		},
	}

	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, nil, te)
		ar, err := newArchiveReader(filePath)
		require.NoError(t, err)

		sdkData, err := ar.GetEnvironmentSDKData(te.id())
		require.NoError(t, err)
		verifyEnvironmentSDKData(t, testEnv1, sdkData)
	})
}

func verifyAllEnvironmentData(t *testing.T, ar *archiveReader) {
	var expectedEnvIDs []config.EnvironmentID
	for _, te := range allTestEnvs {
		expectedEnvIDs = append(expectedEnvIDs, te.id())
	}
	actualEnvIDs := ar.GetEnvironmentIDs()
	sort.Slice(actualEnvIDs, func(i, j int) bool { return actualEnvIDs[i] < actualEnvIDs[j] })
	assert.Equal(t, expectedEnvIDs, actualEnvIDs)

	for _, te := range allTestEnvs {
		envData, err := ar.GetEnvironmentMetadata(te.id())
		require.NoError(t, err)
		verifyEnvironmentParams(t, te, envData.params)
		sdkData, err := ar.GetEnvironmentSDKData(te.id())
		verifyEnvironmentSDKData(t, te, sdkData)
	}
}
