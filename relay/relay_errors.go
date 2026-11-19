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

func errRedisTLSConfig(err error) error {
	return fmt.Errorf("invalid Redis TLS configuration: %w", err)
}

const warnRedisTLSFilesWithoutTLS = "Redis client certificate, key or CA file was set, but TLS is not enabled " +
	"(use REDIS_TLS or a rediss:// URL); these settings will be ignored"

const warnRedisTLSInsecureSkipVerify = "Redis TLS certificate verification is disabled " +
	"(REDIS_TLS_INSECURE_SKIP_VERIFY); the connection is encrypted but the identity of the Redis server is not checked"

const warnRedisTLSInsecureSkipVerifyWithoutTLS = "Redis TLS certificate verification was disabled " +
	"(REDIS_TLS_INSECURE_SKIP_VERIFY), but TLS is not enabled (use REDIS_TLS or a rediss:// URL); this setting will be ignored"
