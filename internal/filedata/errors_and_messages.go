package filedata

import "fmt"

// All log messages, error singletons, and error constructors for this package should be collected here,
// except for debug logging.

const (
	logMsgAddEnv                       = "Added environment %s (%s)"
	logMsgUpdateEnv                    = "Updated environment %s (%s)"
	logMsgDeleteEnv                    = "Removed environment %s (%s)"
	logMsgNoEnvs                       = "The data file does not contain any environments; check your configuration"
	logMsgBadEnvData                   = "Found invalid data for environment %s; skipping this environment"
	logMsgReloadedData                 = "Reloaded data from %s"
	logMsgReloadFileNotFound           = "Data file reload failed; file not found"
	logMsgReloadError                  = "Data file reload failed; file is invalid or possibly incomplete (error: %s)"
	logMsgReloadUnchangedRetry         = "Data file has not changed since last failure, will wait in case it is still being copied"
	logMsgReloadUnchangedNoMoreRetries = "Data file reload failed, and no further changes were detected; giving up until next change (error: %s)"
	logMsgReloadWillRetry              = "Will retry in %s"
)

func errBadItemJSON(key, namespace string) error {
	return fmt.Errorf("found invalid JSON data for key %q in %q", key, namespace)
}

func errCannotOpenArchiveFile(filePath string, err error) error {
	return fmt.Errorf("unable to read file data source %s: %w", filePath, err)
}

func errCreateArchiveManagerFailed(filePath string, err error) error { // COVERAGE: can't cause this condition in unit tests
	return fmt.Errorf("unable to initialize archive manager for %q: %w", filePath, err)
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
