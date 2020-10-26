package config

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/gcfg.v1"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func errLoadingConfigFile(path string, err error) error {
	return fmt.Errorf("failed to read configuration file %q: %w", path, err)
}

// LoadConfigFile reads a configuration file into a Config struct and performs basic validation.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFile(c *Config, path string, loggers ldlog.Loggers) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return errLoadingConfigFile(path, FilterGcfgError(err))
	}

	return ValidateConfig(c, loggers)
}

// FilterGcfgError transforms errors returned by gcfg to our preferred format.
func FilterGcfgError(err error) error {
	gcfgExtraDataErrPhrase := "can't store data at"
	// Make gcfg's messages for unknown sections/fields slightly easier to understand
	if err != nil && strings.Contains(err.Error(), gcfgExtraDataErrPhrase) {
		return errors.New(strings.Replace(err.Error(), gcfgExtraDataErrPhrase, "unsupported or misspelled", 1))
	}
	return err
}
