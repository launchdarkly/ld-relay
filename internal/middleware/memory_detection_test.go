package middleware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadCgroupV2MemoryMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.max")

	require := func(content string) {
		_ = os.WriteFile(path, []byte(content), 0600)
	}

	require("max\n")
	_, ok := readCgroupV2MemoryMax(path)
	assert.False(t, ok, "'max' means no real limit")

	require("8589934592\n")
	v, ok := readCgroupV2MemoryMax(path)
	assert.True(t, ok)
	assert.Equal(t, int64(8589934592), v)

	require("not a number")
	_, ok = readCgroupV2MemoryMax(path)
	assert.False(t, ok)

	_, ok = readCgroupV2MemoryMax(filepath.Join(dir, "does-not-exist"))
	assert.False(t, ok)
}

func TestReadCgroupV1MemoryLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.limit_in_bytes")

	require := func(content string) {
		_ = os.WriteFile(path, []byte(content), 0600)
	}

	// cgroup v1's "no limit set" sentinel is an implausibly large number
	require("9223372036854771712\n")
	_, ok := readCgroupV1MemoryLimit(path)
	assert.False(t, ok)

	require("8589934592\n")
	v, ok := readCgroupV1MemoryLimit(path)
	assert.True(t, ok)
	assert.Equal(t, int64(8589934592), v)
}

func TestReadProcMemTotal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(path, []byte("MemTotal:       16384000 kB\nMemFree:        1000000 kB\n"), 0600)

	v, ok := readProcMemTotal(path)
	assert.True(t, ok)
	assert.Equal(t, int64(16384000*1024), v)

	_, ok = readProcMemTotal(filepath.Join(dir, "does-not-exist"))
	assert.False(t, ok)
}
