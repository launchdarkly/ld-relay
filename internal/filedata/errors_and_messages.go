package filedata

import "fmt"

func errBadItemJSON(key, namespace string) error {
	return fmt.Errorf("found invalid JSON data for key %q in %q", key, namespace)
}

func errCannotOpenArchiveFile(filePath string, err error) error {
	return fmt.Errorf("unable to read file data source %s: %w", filePath, err)
}
func errChecksumDoesNotMatch(expected, actual string) error {
	return fmt.Errorf("checksum of environments did not match: expected %q, got %q", expected, actual)
}

func errChecksumFailed(err error) error { // COVERAGE: can't cause this condition in unit tests
	return fmt.Errorf("unable to compute checksum of environments: %w", err)
}

func errMissingEnvironmentFile(filePath string, err error) error {
	return fmt.Errorf("unable to read %q from archive: %w", filePath, err)
}

func errUncompressedFileTooBig(fileName string, maxSize int64) error {
	return fmt.Errorf("detected malformed or malicious archive file; it contained a file %q with a size >= %d bytes",
		fileName, maxSize)
}
