package entconfig

import (
	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// LoadConfigFromEnvironment sets parameters in an EnterpriseConfig struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *EnterpriseConfig, loggers ldlog.Loggers) error {
	reader := ct.NewVarReaderFromEnvironment()

	baseResult := config.LoadConfigFromEnvironmentBase(&c.Config, loggers)
	for _, e := range baseResult.Errors() {
		reader.AddError(e.Path, e.Err)
	}

	reader.ReadStruct(&c.AutoConfig, false)

	if !reader.Result().OK() {
		return reader.Result().GetError()
	}

	return ValidateConfig(c, loggers)
}
