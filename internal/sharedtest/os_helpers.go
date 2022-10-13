package sharedtest

import (
	"os"
)

// WithTempDir creates a temporary directory, calls the function with its path, then removes it.
func WithTempDir(fn func(path string)) {
	path, err := os.MkdirTemp("", "relay-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(path) //nolint:errcheck
	fn(path)
}
