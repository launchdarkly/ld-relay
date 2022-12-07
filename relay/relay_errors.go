package relay

import (
	"errors"
	"fmt"
)

var (
	errAlreadyClosed         = errors.New("this Relay was already shut down")
	errInitializationTimeout = errors.New("timed out waiting for environments to initialize")
	errSomeEnvironmentFailed = errors.New("one or more environments failed to initialize")
)

func errNewClientContextFailed(envName string, err error) error {
	return fmt.Errorf(`unable to create client context for "%s": %w`, envName, err)
}

func errNewMetricsManagerFailed(err error) error {
	return fmt.Errorf("unable to create metrics manager: %w", err)
}
