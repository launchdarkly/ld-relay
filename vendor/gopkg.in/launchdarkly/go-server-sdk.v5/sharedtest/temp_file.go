package sharedtest

import (
	"io/ioutil"
	"os"
)

// WithTempFileContaining runs the specified function with the file path of a temporary file that has been
// created with the specified data.
func WithTempFileContaining(data []byte, action func(filename string)) {
	f, err := ioutil.TempFile("", "test-file")
	if err != nil {
		panic(err)
	}
	_, err = f.Write(data)
	if err != nil {
		panic(err)
	}
	err = f.Close()
	if err != nil {
		panic(err)
	}
	filename := f.Name()
	defer func() {
		_ = os.Remove(filename)
	}()
	action(filename)
}
