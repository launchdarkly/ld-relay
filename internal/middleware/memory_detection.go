package middleware

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// detectAvailableMemoryBytes returns the memory limit that applies to this process: the
// cgroup limit if one is set (cgroup v2, then v1), otherwise total system memory. Returns
// (0, false) if neither can be determined (e.g. non-Linux), so callers can fall back to a
// safe default rather than guessing.
func detectAvailableMemoryBytes() (int64, bool) {
	if v, ok := readCgroupV2MemoryMax("/sys/fs/cgroup/memory.max"); ok {
		return v, true
	}
	if v, ok := readCgroupV1MemoryLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
		return v, true
	}
	if v, ok := readProcMemTotal("/proc/meminfo"); ok {
		return v, true
	}
	return 0, false
}

// path is always one of a fixed set of well-known system paths, parameterized only so
// tests can point at a temp file; there is no attacker-controlled input here.
func readCgroupV2MemoryMax(path string) (int64, bool) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// path is always a fixed system path; parameterized only for testability, see above.
func readCgroupV1MemoryLimit(path string) (int64, bool) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	// cgroup v1 reports an effectively-unlimited sentinel (close to the max int64/pagesize
	// product) when no real limit is set; treat anything implausibly large as "no limit".
	const implausiblyLarge = int64(1) << 62
	if v >= implausiblyLarge {
		return 0, false
	}
	return v, true
}

// path is always a fixed system path; parameterized only for testability, see above.
func readProcMemTotal(path string) (int64, bool) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
