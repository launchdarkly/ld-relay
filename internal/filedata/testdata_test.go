package filedata

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"

	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEnv struct {
	rep     envfactory.EnvironmentRep
	dataID  string
	sdkData map[string]map[string]interface{}
}

var testEnv1 = testEnv{
	rep: envfactory.EnvironmentRep{
		EnvID:    config.EnvironmentID("1111111111"),
		ProjName: "Project1",
		ProjKey:  "project1",
		EnvName:  "Env1",
		EnvKey:   "env1",
		Version:  1,
	},
	dataID: "1000",
	sdkData: map[string]map[string]interface{}{
		"flags": map[string]interface{}{
			"env1Flag1": ldbuilders.NewFlagBuilder("env1Flag1").Version(1).On(true).Build(),
			"env1Flag2": ldbuilders.NewFlagBuilder("env1Flag2").Version(1).On(false).Build(),
		},
	},
}

var testEnv2 = testEnv{
	rep: envfactory.EnvironmentRep{
		EnvID:      config.EnvironmentID("2222222222"),
		ProjName:   "Project1",
		ProjKey:    "project1",
		EnvName:    "Env1",
		EnvKey:     "env1",
		SecureMode: true,
		Version:    1,
	},
	dataID: "2000",
	sdkData: map[string]map[string]interface{}{
		"flags": map[string]interface{}{
			"env2Flag1": ldbuilders.NewFlagBuilder("env2Flag1").Version(1).On(true).Build(),
		},
		"segments": map[string]interface{}{
			"env2Segment1": ldbuilders.NewSegmentBuilder("env2Segment1").Version(1).Build(),
		},
	},
}

var allTestEnvs = []testEnv{testEnv1, testEnv2}

func (te testEnv) id() config.EnvironmentID {
	return te.rep.EnvID
}

func (te testEnv) sdkDataJSON() []byte {
	data, err := json.Marshal(te.sdkData)
	if err != nil {
		panic(err)
	}
	return data
}

func (te testEnv) withMetadataChange() testEnv {
	ret := te
	ret.rep.Version++
	ret.rep.EnvKey += "-mod"
	return ret
}

func (te testEnv) withSDKDataChange() testEnv {
	ret := te
	ret.dataID += "-mod"
	return ret
}

func (te testEnv) withoutSDKData() testEnv {
	ret := te
	ret.sdkData = nil
	return ret
}

func sortTestEnvs(envs []testEnv) []testEnv {
	ret := make([]testEnv, len(envs))
	copy(ret, envs)
	sort.Slice(ret, func(i, j int) bool { return ret[i].rep.EnvID < ret[j].rep.EnvID })
	return ret
}

func verifyEnvironmentData(t *testing.T, te testEnv, env ArchiveEnvironment) {
	verifyEnvironmentParams(t, te, env.Params)
	verifyEnvironmentSDKData(t, te, env.SDKData)
}

func verifyEnvironmentParams(t *testing.T, te testEnv, envParams envfactory.EnvironmentParams) {
	assert.Equal(t, te.rep.ToParams(), envParams)
}

func verifyEnvironmentSDKData(t *testing.T, te testEnv, sdkData []ldstoretypes.Collection) {
	if te.sdkData == nil {
		assert.Nil(t, sdkData)
		return
	}
	sdkDataMap := make(map[string]map[string]interface{})
	for _, coll := range sdkData {
		kindName := coll.Kind.GetName()
		if kindName == "features" {
			kindName = "flags"
		}
		itemsMap := make(map[string]interface{})
		for _, item := range coll.Items {
			itemsMap[item.Key] = json.RawMessage(coll.Kind.Serialize(item.Item))
		}
		sdkDataMap[kindName] = itemsMap
	}
	actualSDKDataJSON, err := json.Marshal(sdkDataMap)
	require.NoError(t, err)
	assert.JSONEq(t, string(te.sdkDataJSON()), string(actualSDKDataJSON))
}

func withTestData(fn func(dirPath string), envs ...testEnv) {
	sharedtest.WithTempDir(func(dirPath string) {
		h := md5.New()
		var envIDs []config.EnvironmentID
		for _, te := range envs {
			envIDs = append(envIDs, te.rep.EnvID)
			rep := archiveEnvironmentRep{
				Env:    te.rep,
				DataID: te.dataID,
			}
			fileData, err := json.Marshal(rep)
			if err != nil {
				panic(err)
			}
			ioutil.WriteFile(envMetadataFilePath(dirPath, te.rep.EnvID), fileData, 0600)
			ioutil.WriteFile(envSDKDataFilePath(dirPath, te.rep.EnvID), te.sdkDataJSON(), 0600)
			h.Write(fileData)
		}
		checksum, err := computeEnvironmentsChecksum(dirPath, envIDs)
		if err != nil {
			panic(err)
		}
		err = ioutil.WriteFile(checksumFilePath(dirPath), checksum, 0600)
		if err != nil {
			panic(err)
		}

		fn(dirPath)
	})
}

func writeArchive(t *testing.T, filePath string, compressed bool, modifyFn func(dirPath string), envs ...testEnv) {
	_ = os.Remove(filePath)

	destFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)

	var tarWriter *tar.Writer
	var gzWriter *gzip.Writer
	if compressed {
		gzWriter = gzip.NewWriter(destFile)
		tarWriter = tar.NewWriter(gzWriter)
	} else {
		tarWriter = tar.NewWriter(destFile)
	}

	withTestData(func(dirPath string) {
		if modifyFn != nil {
			modifyFn(dirPath)
		}

		filepath.Walk(dirPath, func(file string, fi os.FileInfo, err error) error {
			// In this implementation we never have subdirectories in the archive
			if fi.IsDir() {
				return nil
			}
			h, err := tar.FileInfoHeader(fi, fi.Name())
			if err != nil {
				panic(err)
			}
			require.NoError(t, tarWriter.WriteHeader(h))
			f, err := os.Open(file)
			require.NoError(t, err)
			defer f.Close()
			_, err = io.Copy(tarWriter, f)
			require.NoError(t, err)
			return nil
		})
	}, envs...)

	tarWriter.Flush()
	tarWriter.Close()
	if gzWriter != nil {
		gzWriter.Flush()
		gzWriter.Close()
	}
	destFile.Close()

	fileInfo, _ := os.Stat(filePath)
	fmt.Printf("wrote test archive (%d bytes) to %s\n", fileInfo.Size(), filePath)
}

func writeMalformedArchive(filePath string) {
	_ = os.Remove(filePath)
	data := []byte("not valid")
	err := ioutil.WriteFile(filePath, data, 0600)
	if err != nil {
		panic(err)
	}
	fmt.Printf("wrote deliberately invalid test archive (%d bytes) to %s\n", len(data), filePath)
}

func removeChecksumFileFromArchive(dirPath string) {
	err := os.Remove(checksumFilePath(dirPath))
	if err != nil {
		panic(err)
	}
}

func makeChecksumFileInvalidInArchive(dirPath string) {
	f, err := os.OpenFile(checksumFilePath(dirPath), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	_, err = f.Write([]byte{1})
	if err != nil {
		panic(err)
	}
}

func rehash(dirPath string, envIDs ...config.EnvironmentID) {
	newHash, err := computeEnvironmentsChecksum(dirPath, envIDs)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(checksumFilePath(dirPath), newHash, 0660)
	if err != nil {
		panic(err)
	}
}
