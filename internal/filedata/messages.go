package filedata

import "fmt"

// All log messages, error singletons, and error constructors for this package should be collected here,
// except for debug logging.

const (
	logMsgAddEnv                       = "Added environment %s (%s)"
	logMsgUpdateEnv                    = "Updated environment %s (%s)"
	logMsgDeleteEnv                    = "Removing environment %s (%s)"
	logMsgNoEnvs                       = "The data file does not contain any environments; check your configuration"
	logMsgBadEnvData                   = "Found invalid data for environment %s; skipping this environment"
	logMsgReloadedData                 = "Reloaded data from %s"
	logMsgReloadFileNotFound           = "Data file reload failed; file not found, will retry"
	logMsgReloadError                  = "Data file reload failed; file is invalid or possibly incomplete, will retry (error: %s)"
	logMsgReloadUnchangedRetry         = "Data file has not changed since last failure, will wait and retry in case it is still being copied"
	logMsgReloadUnchangedNoMoreRetries = "Data file reload failed, and no further changes were detected; giving up until next change (error: %s)"
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
