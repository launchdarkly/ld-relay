package entconfig

import (
	"fmt"

	config "github.com/launchdarkly/ld-relay-config"

	"github.com/go-gcfg/gcfg"
)

// LoadConfigFile reads a configuration file into an EnterpriseConfig struct and performs basic validation.
//
// The Config parameter could be initialized with default values first, but does not need to be.
func LoadConfigFile(c *EnterpriseConfig, path string) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return fmt.Errorf(`failed to read configuration file "%s": %w`, path, config.FilterGcfgError(err))
	}

	return ValidateConfig(c)
}
