package entconfig

import (
	config "github.com/launchdarkly/ld-relay-config"

	ct "github.com/launchdarkly/go-configtypes"
)

// LoadConfigFromEnvironment sets parameters in an EnterpriseConfig struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *EnterpriseConfig) error {
	reader := ct.NewVarReaderFromEnvironment()

	baseResult := config.LoadConfigFromEnvironmentBase(&c.Config)
	for _, e := range baseResult.Errors() {
		reader.AddError(e.Path, e.Err)
	}

	reader.ReadStruct(&c.AutoConfig, false)

	if !reader.Result().OK() {
		return reader.Result().GetError()
	}

	return ValidateConfig(c)
}
