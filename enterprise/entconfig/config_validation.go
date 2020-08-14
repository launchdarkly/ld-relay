package entconfig

import (
	"errors"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var (
	errAutoConfPropertiesWithNoKey = errors.New("must specify auto-configuration key if other auto-configuration properties are set")
	errAutoConfWithEnvironments    = errors.New("cannot configure specific environments if auto-configuration is enabled")
)

// ValidateConfig ensures that the configuration does not contain contradictory properties.
//
// This method covers validation rules that can't be enforced on a per-field basis (for instance, if
// either field A or field B can be specified but it's invalid to specify both). It is allowed to modify
// the Config struct in order to canonicalize settings in a way that simplifies things for the Relay code
// (for instance, converting Redis host/port settings into a Redis URL, or converting deprecated fields to
// non-deprecated ones).
//
// LoadConfigFromEnvironment and LoadConfigFromFile both call this method as a last step.
func ValidateConfig(c *EnterpriseConfig, loggers ldlog.Loggers) error {
	var result ct.ValidationResult

	baseError := config.ValidateConfig(&c.Config, loggers)
	if baseError != nil {
		if ae, ok := baseError.(ct.ValidationAggregateError); ok {
			for _, e := range ae {
				result.AddError(e.Path, e.Err)
			}
		} else if e, ok := baseError.(ct.ValidationError); ok {
			result.AddError(e.Path, e.Err)
		} else {
			result.AddError(nil, baseError)
		}
	}

	if c.AutoConfig.Key == "" {
		if c.AutoConfig.EnvDatastorePrefix != "" || c.AutoConfig.EnvDatastoreTableName != "" ||
			len(c.AutoConfig.EnvAllowedOrigin.Values()) != 0 {
			result.AddError(nil, errAutoConfPropertiesWithNoKey)
		}
	} else if len(c.Environment) != 0 {
		result.AddError(nil, errAutoConfWithEnvironments)
	}

	return result.GetError()
}