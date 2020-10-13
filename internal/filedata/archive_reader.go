package filedata

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/autoconfig"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

const (
	environmentsChecksumFileName = "checksum.md5"
)

// archiveReader is the low-level implementation of unarchiving a data file and reading the environments.
// We only keep this object around for as long as it takes to read all of the environment data.
type archiveReader struct {
	dirPath        string
	environmentIDs []config.EnvironmentID
}

type environmentMetadata struct {
	params  EnvironmentParams
	version int
	dataID  string
}

type archiveEnvironmentRep struct {
	autoconfig.EnvironmentRep
	DataID string `json:"dataId"`
}

func envMetadataFilePath(dirPath string, envID config.EnvironmentID) string {
	return filepath.Join(dirPath, fmt.Sprintf("%s.json", string(envID)))
}

func envSDKDataFilePath(dirPath string, envID config.EnvironmentID) string {
	return filepath.Join(dirPath, fmt.Sprintf("%s-data.json", string(envID)))
}

func checksumFilePath(dirPath string) string {
	return filepath.Join(dirPath, environmentsChecksumFileName)
}

func isMetadataFileName(filename string) bool {
	return strings.HasSuffix(filename, ".json") && !strings.HasSuffix(filename, "-data.json")
}

func getEnvIDFromMetadataFileName(filename string) config.EnvironmentID {
	return config.EnvironmentID(strings.TrimSuffix(filename, ".json"))
}

func newArchiveReader(filePath string) (*archiveReader, error) {
	dirPath, err := ioutil.TempDir("", "ld-relay-******")
	if err != nil {
		return nil, err // COVERAGE: can't cause this condition in unit tests (unexpected OS error)
	}
	if err := readCompressedArchive(filePath, dirPath); err != nil {
		if err := readUncompressedArchive(filePath, dirPath); err != nil {
			return nil, err
		}
	}
	envIDs := discoverEnvironmentIDs(dirPath)
	expectedChecksum, err := ioutil.ReadFile(checksumFilePath(dirPath))
	if err != nil {
		return nil, errMissingEnvironmentFile(environmentsChecksumFileName, err)
	}
	actualChecksum, err := computeEnvironmentsChecksum(dirPath, envIDs)
	if err != nil {
		return nil, errChecksumFailed(err) // COVERAGE: can't cause this condition in unit tests (unexpected failure of md5 package)
	}
	if bytes.Compare(expectedChecksum, actualChecksum) != 0 {
		return nil, errChecksumDoesNotMatch(hex.EncodeToString(expectedChecksum), hex.EncodeToString(actualChecksum))
	}
	return &archiveReader{
		dirPath:        dirPath,
		environmentIDs: envIDs,
	}, nil
}

func (ar *archiveReader) Close() {
	_ = os.RemoveAll(ar.dirPath)
}

func (ar *archiveReader) GetEnvironmentIDs() []config.EnvironmentID {
	return ar.environmentIDs
}

func (ar *archiveReader) GetEnvironmentMetadata(envID config.EnvironmentID) (environmentMetadata, error) {
	data, err := ioutil.ReadFile(envMetadataFilePath(ar.dirPath, envID))
	if err != nil {
		return environmentMetadata{}, err // COVERAGE: should be impossible if the checksum passed
	}
	var rep archiveEnvironmentRep
	if err := json.Unmarshal(data, &rep); err != nil {
		return environmentMetadata{}, err
	}
	return environmentMetadata{
		params:  EnvironmentParams(autoconfig.MakeEnvironmentParams(rep.EnvironmentRep)),
		version: rep.Version,
		dataID:  rep.DataID,
	}, nil
}

func (ar *archiveReader) GetEnvironmentSDKData(envID config.EnvironmentID) ([]ldstoretypes.Collection, error) {
	data, err := ioutil.ReadFile(envSDKDataFilePath(ar.dirPath, envID))
	if err != nil {
		return nil, err // COVERAGE: should be impossible if the checksum passed
	}
	var allData map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &allData); err != nil {
		return nil, err
	}
	// We'll deserialize the flags and segments one item at a time so we can provide a more useful
	// error message if one of them is malformed.
	var ret []ldstoretypes.Collection
	for kindName, valuesMap := range allData {
		var kind ldstoretypes.DataKind
		switch kindName {
		case "flags":
			kind = ldstoreimpl.Features()
		case "segments":
			kind = ldstoreimpl.Segments()
		default:
			continue
		}
		coll := ldstoretypes.Collection{Kind: kind, Items: make([]ldstoretypes.KeyedItemDescriptor, 0, len(valuesMap))}
		for key, valueJSON := range valuesMap {
			item, err := kind.Deserialize(valueJSON)
			if err != nil {
				return nil, errBadItemJSON(key, kindName)
			}
			coll.Items = append(coll.Items, ldstoretypes.KeyedItemDescriptor{Key: key, Item: item})
		}
		ret = append(ret, coll)
	}
	return ret, nil
}

func readCompressedArchive(filePath, targetDir string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	return readTar(gr, targetDir)
}

func readUncompressedArchive(filePath, targetDir string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return readTar(f, targetDir)
}

func readTar(r io.Reader, targetDir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		// In our archive format, there should be no subdirectories, just top-level files
		if h.Typeflag == tar.TypeReg {
			outFile, err := os.OpenFile(filepath.Join(targetDir, h.Name), os.O_CREATE|os.O_RDWR, os.FileMode(h.Mode))
			if err != nil {
				return err // COVERAGE: can't cause this condition in unit tests
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}
}

func discoverEnvironmentIDs(dirPath string) []config.EnvironmentID {
	files, _ := ioutil.ReadDir(dirPath) // should never fail, but if it does, files will be nil anyway
	var ret []config.EnvironmentID
	for _, file := range files {
		if isMetadataFileName(file.Name()) {
			ret = append(ret, getEnvIDFromMetadataFileName(file.Name()))
		}
	}
	return ret
}

func computeEnvironmentsChecksum(dirPath string, envIDs []config.EnvironmentID) ([]byte, error) {
	filePaths := make([]string, 0, len(envIDs)*2)
	for _, envID := range envIDs {
		filePaths = append(filePaths, envMetadataFilePath(dirPath, envID))
		filePaths = append(filePaths, envSDKDataFilePath(dirPath, envID))
	}
	sort.Strings(filePaths)
	h := md5.New()
	for _, path := range filePaths {
		if err := addFileToHash(h, path); err != nil {
			return nil, err // COVERAGE: can't cause this condition in unit tests
		}
	}
	return h.Sum(nil), nil
}

func addFileToHash(h hash.Hash, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err // COVERAGE: can't cause this condition in unit tests
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}
