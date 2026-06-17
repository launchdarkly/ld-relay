package configsource

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // MD5 is used only for change-detection, not authentication
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	helpers "github.com/launchdarkly/go-test-helpers/v3"
)

// ArchiveEnvSpec describes one environment to be written into an offline-mode archive.
// Flags and Segments follow the same JSON shape as the archive data files; values should be
// JSON-serializable objects (e.g. from ldbuilders). Both are optional.
type ArchiveEnvSpec struct {
	// Rep is the EnvironmentRep written to the metadata file.
	Rep envfactory.EnvironmentRep
	// DataID is an opaque string stored alongside the metadata. Relay uses it to detect whether
	// the data file changed across reloads. Any non-empty string works.
	DataID string
	// Flags is a map of flag key → JSON-serializable flag object. May be nil.
	Flags map[string]any
	// Segments is a map of segment key → JSON-serializable segment object. May be nil.
	Segments map[string]any
}

// ArchiveFixtureBuilder builds offline-mode archive files (.tar.gz) for use as Relay's
// FileDataSource. The archive format matches what internal/filedata.ArchiveManager expects:
// an {envID}.json metadata file, an {envID}-data.json flag/segment data file, and a checksum.md5
// file. Call AddEnv one or more times, then WriteTempFile or WriteFile.
type ArchiveFixtureBuilder struct {
	envs []ArchiveEnvSpec
}

// NewArchiveFixtureBuilder creates an empty builder.
func NewArchiveFixtureBuilder() *ArchiveFixtureBuilder {
	return &ArchiveFixtureBuilder{}
}

// AddEnv adds an environment to the archive. Returns the builder for chaining.
func (b *ArchiveFixtureBuilder) AddEnv(spec ArchiveEnvSpec) *ArchiveFixtureBuilder {
	b.envs = append(b.envs, spec)
	return b
}

// WriteTempFile writes the archive to a temporary .tar.gz file and returns its path. The file is
// removed automatically when the test ends.
func (b *ArchiveFixtureBuilder) WriteTempFile(t testing.TB) string {
	t.Helper()
	f, err := os.CreateTemp("", "ld-relay-archive-*.tar.gz")
	if err != nil {
		t.Fatalf("ArchiveFixtureBuilder: create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(path) })
	b.WriteFile(t, path)
	return path
}

// WriteFile writes the archive to the given path as a .tar.gz file.
func (b *ArchiveFixtureBuilder) WriteFile(t testing.TB, path string) {
	t.Helper()

	// Stage files in a temp directory, compute checksum, then tar.gz the result.
	helpers.WithTempDir(func(dir string) {
		for _, spec := range b.envs {
			b.writeEnvFiles(t, dir, spec)
		}
		envIDs := make([]config.EnvironmentID, 0, len(b.envs))
		for _, spec := range b.envs {
			envIDs = append(envIDs, spec.Rep.EnvID)
		}
		writeArchiveChecksum(t, dir, envIDs)
		writeArchiveTarGz(t, path, dir)
	})
}

// archiveEnvRep mirrors the unexported filedata.archiveEnvironmentRep JSON structure.
type archiveEnvRep struct {
	Env    envfactory.EnvironmentRep `json:"env"`
	DataID string                    `json:"dataId"`
}

func (b *ArchiveFixtureBuilder) writeEnvFiles(t testing.TB, dir string, spec ArchiveEnvSpec) {
	t.Helper()

	// {envID}.json
	metaBytes, err := json.Marshal(archiveEnvRep{Env: spec.Rep, DataID: spec.DataID})
	if err != nil {
		t.Fatalf("ArchiveFixtureBuilder: marshal env metadata: %v", err)
	}
	writeArchiveFile(t, archiveMetadataPath(dir, spec.Rep.EnvID), metaBytes)

	// {envID}-data.json
	sdkData := make(map[string]any, 2)
	if len(spec.Flags) > 0 {
		sdkData["flags"] = spec.Flags
	}
	if len(spec.Segments) > 0 {
		sdkData["segments"] = spec.Segments
	}
	dataBytes, err := json.Marshal(sdkData)
	if err != nil {
		t.Fatalf("ArchiveFixtureBuilder: marshal sdk data: %v", err)
	}
	writeArchiveFile(t, archiveDataPath(dir, spec.Rep.EnvID), dataBytes)
}

func writeArchiveChecksum(t testing.TB, dir string, envIDs []config.EnvironmentID) {
	t.Helper()
	paths := make([]string, 0, len(envIDs)*2)
	for _, id := range envIDs {
		paths = append(paths, archiveMetadataPath(dir, id), archiveDataPath(dir, id))
	}
	sort.Strings(paths)

	h := md5.New() //nolint:gosec
	for _, p := range paths {
		f, err := os.Open(filepath.Clean(p))
		if err != nil {
			t.Fatalf("ArchiveFixtureBuilder: open for checksum %s: %v", p, err)
		}
		if _, err = io.Copy(h, f); err != nil {
			_ = f.Close()
			t.Fatalf("ArchiveFixtureBuilder: hash %s: %v", p, err)
		}
		_ = f.Close()
	}
	writeArchiveFile(t, filepath.Join(dir, "checksum.md5"), h.Sum(nil))
}

func writeArchiveTarGz(t testing.TB, destPath, srcDir string) {
	t.Helper()
	_ = os.Remove(destPath)
	f, err := os.OpenFile(filepath.Clean(destPath), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("ArchiveFixtureBuilder: create archive %s: %v", destPath, err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ArchiveFixtureBuilder: read staging dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(srcDir, entry.Name())
		fi, err := os.Stat(srcPath)
		if err != nil {
			t.Fatalf("ArchiveFixtureBuilder: stat %s: %v", srcPath, err)
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			t.Fatalf("ArchiveFixtureBuilder: tar header for %s: %v", entry.Name(), err)
		}
		hdr.Name = entry.Name() // strip any directory prefix
		if err = tw.WriteHeader(hdr); err != nil {
			t.Fatalf("ArchiveFixtureBuilder: write tar header: %v", err)
		}
		src, err := os.Open(filepath.Clean(srcPath))
		if err != nil {
			t.Fatalf("ArchiveFixtureBuilder: open %s: %v", srcPath, err)
		}
		if _, err = io.Copy(tw, src); err != nil {
			_ = src.Close()
			t.Fatalf("ArchiveFixtureBuilder: copy %s into tar: %v", entry.Name(), err)
		}
		_ = src.Close()
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("ArchiveFixtureBuilder: close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("ArchiveFixtureBuilder: close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("ArchiveFixtureBuilder: close file: %v", err)
	}
}

func archiveMetadataPath(dir string, id config.EnvironmentID) string {
	return filepath.Join(dir, fmt.Sprintf("%s.json", string(id)))
}

func archiveDataPath(dir string, id config.EnvironmentID) string {
	return filepath.Join(dir, fmt.Sprintf("%s-data.json", string(id)))
}

func writeArchiveFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Clean(path), data, 0600); err != nil {
		t.Fatalf("ArchiveFixtureBuilder: write %s: %v", path, err)
	}
}
