package config

import (
	"fmt"

	"github.com/launchdarkly/gcfg"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// LoadConfigFile reads a configuration file into a Config struct and performs basic validation.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFile(c *Config, path string, loggers ldlog.Loggers) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return fmt.Errorf(`failed to read configuration file "%s": %s`, path, err)
	}

	return ValidateConfig(c, loggers)
}
