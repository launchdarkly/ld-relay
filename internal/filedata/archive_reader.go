package filedata

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // we're not using this weak algorithm for authentication, only for detecting file changes
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
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"

	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
)

const (
	environmentsChecksumFileName = "checksum.md5"
	maxDecompressedFileSize      = 1024 * 1024 * 200 // arbitrary 200MB limit to avoid decompression bombs
)

// archiveReader is the low-level implementation of unarchiving a data file and reading the environments.
// We only keep this object around for as long as it takes to read all of the environment data.
type archiveReader struct {
	dirPath        string
	environmentIDs []config.EnvironmentID
}

type environmentMetadata struct {
	params  envfactory.EnvironmentParams
	version int
	dataID  string
}

type archiveEnvironmentRep struct {
	Env    envfactory.EnvironmentRep `json:"env"`
	DataID string                    `json:"dataId"`
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

// newArchiveReader attempts to expand an archive file, which can be either a .tar or a .tar.gz. The
// contents are copied to a temporary directory.
//
// It verifies the checksum, but does not try to read the individual environment data until you call
// GetEnvironmentMetadata or GetEnvironmentSDKData.
func newArchiveReader(filePath string) (*archiveReader, error) {
	dirPath, err := ioutil.TempDir("", "ld-relay-")
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
	if !bytes.Equal(expectedChecksum, actualChecksum) {
		return nil, errChecksumDoesNotMatch(hex.EncodeToString(expectedChecksum), hex.EncodeToString(actualChecksum))
	}
	return &archiveReader{
		dirPath:        dirPath,
		environmentIDs: envIDs,
	}, nil
}

// Close disposes of the temporary directory that was created by this archiveReader.
func (ar *archiveReader) Close() {
	_ = os.RemoveAll(ar.dirPath)
}

// GetEnvironmentIDs returns all of the environment IDs contained in the archive. These are detected
// by simply looking for all filenames in the format "$ENVID.json".
func (ar *archiveReader) GetEnvironmentIDs() []config.EnvironmentID {
	return ar.environmentIDs
}

// GetEnvironmentMetadata attempts to read the "$ENVID.json" file for the specified environment.
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
		params:  rep.Env.ToParams(),
		version: rep.Env.Version,
		dataID:  rep.DataID,
	}, nil
}

// GetEnvironmentSDKData attempts to read the "$ENVID-data.json" file for the specified environment,
// which contains the flag/segment data. It returns the parsed data in the format used by the SDK.
//
// This is a separate step from GetEnvironmentMetadata because when an archive file is updated, the
// data might not have changed for all environments. We check the metadata first, and if the DataID
// property has not changed then we won't bother re-parsing the SDK data.
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
	ret := make([]ldstoretypes.Collection, 0, len(allData))
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
	f, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return err
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return err
	}
	err = readTar(gr, targetDir)
	_ = gr.Close()
	_ = f.Close()
	return err
}

func readUncompressedArchive(filePath, targetDir string) error {
	f, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return err
	}
	err = readTar(f, targetDir)
	_ = f.Close()
	return err
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
		if h.Typeflag != tar.TypeReg {
			continue
		}
		outPath := filepath.Join(targetDir, h.Name)
		outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR, os.FileMode(h.Mode))
		if err != nil {
			return err // COVERAGE: can't cause this condition in unit tests
		}
		bytesCopied, err := io.CopyN(outFile, tr, maxDecompressedFileSize)
		_ = outFile.Close()
		if bytesCopied >= maxDecompressedFileSize {
			_ = os.Remove(outPath)
			return errUncompressedFileTooBig(h.Name, maxDecompressedFileSize)
		}
		if err != nil && err != io.EOF {
			return err
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
	h := md5.New() //nolint:gosec // we're not using this weak algorithm for authentication, only for detecting file changes
	for _, path := range filePaths {
		if err := addFileToHash(h, path); err != nil {
			return nil, err // COVERAGE: can't cause this condition in unit tests
		}
	}
	return h.Sum(nil), nil
}

func addFileToHash(h hash.Hash, filePath string) error {
	f, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return err // COVERAGE: can't cause this condition in unit tests
	}
	_, err = io.Copy(h, f)
	_ = f.Close()
	return err
}
