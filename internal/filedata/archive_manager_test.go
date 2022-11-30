package filedata

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	helpers "github.com/launchdarkly/go-test-helpers/v2"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
)

func TestStartWithValidFile(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, allTestEnvs...)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(allTestEnvs...)
	})
}

func TestStartWithMissingFile(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {}, func(p archiveManagerTestParams) {
		require.Error(t, p.archiveManagerError)
		assert.Contains(t, p.archiveManagerError.Error(), "unable to read file data source")
		assert.Contains(t, p.archiveManagerError.Error(), "no such file")
	})
}

func TestStartWithMalformedFile(t *testing.T) {
	archiveManagerTest(t, writeMalformedArchive, func(p archiveManagerTestParams) {
		require.Error(t, p.archiveManagerError)
	})
}

func TestStartWithFileWithBadChecksum(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, makeChecksumFileInvalidInArchive, allTestEnvs...)
	}, func(p archiveManagerTestParams) {
		require.Error(t, p.archiveManagerError)
		assert.Contains(t, p.archiveManagerError.Error(), "checksum of environments did not match")
	})
}

func TestStartWithFileWithNoEnvironments(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)
		p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, logMsgNoEnvs)
	})
}

func TestStartWithFileWithOneBadEnvironment(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, func(dirPath string) {
			badData := []byte("that doesn't look like anything to me")
			require.NoError(t, os.WriteFile(envMetadataFilePath(dirPath, testEnv1.rep.EnvID), badData, 0660))
			rehash(dirPath, testEnv1.rep.EnvID, testEnv2.rep.EnvID)
		}, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)
		p.mockLog.AssertMessageMatch(t, true, ldlog.Error, fmt.Sprintf(logMsgBadEnvData, testEnv1.rep.EnvID))
		p.expectEnvironmentsAdded(testEnv2)
	})
}

func TestDefaultRetryInterval(t *testing.T) {
	helpers.WithTempFile(func(filePath string) {
		writeArchive(t, filePath, false, nil)

		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		messageHandler := newTestMessageHandler()

		archiveManager, err := NewArchiveManager(
			filePath,
			messageHandler,
			0,
			mockLog.Loggers,
		)
		require.NoError(t, err)
		defer archiveManager.Close()

		assert.Equal(t, defaultRetryInterval, archiveManager.retryInterval)
	})
}

func TestFileUpdatedWithValidDataAddedEnvironment(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1)

		writeArchive(t, p.filePath, false, nil, testEnv1, testEnv2)

		p.expectEnvironmentsAdded(testEnv2)
		p.expectReloaded()
	})
}

func TestFileUpdatedWithValidDataUpdatedEnvironmentMetadataOnly(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		testEnv1a := testEnv1.withMetadataChange()
		writeArchive(t, p.filePath, false, nil, testEnv1a, testEnv2)

		p.expectEnvironmentsUpdated(testEnv1a.withoutSDKData())
		p.expectReloaded()
	})
}

func TestFileUpdatedWithValidDataUpdatedEnvironmentSDKData(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		testEnv1a := testEnv1.withSDKDataChange()
		writeArchive(t, p.filePath, false, nil, testEnv1a, testEnv2)

		p.expectEnvironmentsUpdated(testEnv1a)
		p.expectReloaded()
	})
}

func TestFileUpdatedWithValidDataDeletedEnvironment(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		writeArchive(t, p.filePath, false, nil, testEnv2)

		p.expectEnvironmentsDeleted(testEnv1.rep.EnvID)
		p.expectReloaded()
	})
}

func TestFileUpdatedWithInvalidDataAndDoesNotBecomeValid(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		writeMalformedArchive(p.filePath)

		requireLogMessage(t, p.mockLog, ldlog.Error, "giving up until next change")

		p.requireNoMoreMessages()
	})
}

func TestFileUpdatedWithInvalidDataAndThenValidData(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		writeMalformedArchive(p.filePath)

		requireLogMessage(t, p.mockLog, ldlog.Warn, "file is invalid")

		testEnv1a := testEnv1.withMetadataChange().withSDKDataChange()
		writeArchive(t, p.filePath, false, nil, testEnv1a, testEnv2)

		// Because writeArchive updates the file in-place, we expect several file watch events to be triggered
		// in a row as the data is written incrementally, so this should exercise our logic for retrying after
		// consecutive errors.

		p.expectEnvironmentsUpdated(testEnv1a)
		p.expectReloaded()
	})
}

func TestFileDeletedAndThenRecreatedWithValidData(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		require.NoError(t, os.Remove(p.filePath))

		requireLogMessage(t, p.mockLog, ldlog.Warn, "file not found")

		testEnv1a := testEnv1.withMetadataChange().withSDKDataChange()
		writeArchive(t, p.filePath, false, nil, testEnv1a, testEnv2)

		p.expectEnvironmentsUpdated(testEnv1a)
		p.expectReloaded()
	})
}

func TestFileDeletedAndThenRecreatedWithInvalidDataAndThenValidData(t *testing.T) {
	archiveManagerTest(t, func(filePath string) {
		writeArchive(t, filePath, false, nil, testEnv1, testEnv2)
	}, func(p archiveManagerTestParams) {
		require.NoError(t, p.archiveManagerError)

		p.expectEnvironmentsAdded(testEnv1, testEnv2)

		require.NoError(t, os.Remove(p.filePath))

		requireLogMessage(t, p.mockLog, ldlog.Warn, "file not found")

		writeMalformedArchive(p.filePath)

		requireLogMessage(t, p.mockLog, ldlog.Warn, "file is invalid")

		testEnv1a := testEnv1.withMetadataChange().withSDKDataChange()
		writeArchive(t, p.filePath, false, nil, testEnv1a, testEnv2)

		p.expectEnvironmentsUpdated(testEnv1a)
		p.expectReloaded()
	})
}

func requireLogMessage(t *testing.T, mockLog *ldlogtest.MockLog, level ldlog.LogLevel, expectedSubstring string) {
	require.Eventuallyf(t, func() bool {
		warnings := mockLog.GetOutput(level)
		for _, w := range warnings {
			if strings.Contains(w, expectedSubstring) {
				return true
			}
		}
		return false
	}, time.Second*5, time.Millisecond*100, "wanted log message (%s) containing %q", level, expectedSubstring)
}
