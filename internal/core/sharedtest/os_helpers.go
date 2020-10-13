package sharedtest

import (
	"io/ioutil"
	"os"
)

func WithTempDir(fn func(path string)) {
	path, err := ioutil.TempDir("", "relay-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(path)
	fn(path)
}
