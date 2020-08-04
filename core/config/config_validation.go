package config

import (
	"errors"
	"fmt"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func errEnvironmentWithNoSDKKey(envName string) error {
	return fmt.Errorf("SDK key is required for environment %q", envName)
}

func errMultipleDatabases(databases []string) error {
	return fmt.Errorf("multiple databases are enabled (%s); only one is allowed", strings.Join(databases, ", "))
}

// ValidateConfig ensures that the configuration does not contain contradictory properties.
//
// This method covers validation rules that can't be enforced on a per-field basis (for instance, if
// either field A or field B can be specified but it's invalid to specify both). It is allowed to modify
// the Config struct in order to canonicalize settings in a way that simplifies things for the Relay code
// (for instance, converting Redis host/port settings into a Redis URL, or converting deprecated fields to
// non-deprecated ones).
//
// LoadConfigFromEnvironment and LoadConfigFromFile both call this method as a last step, but it is also
// called again by the Relay constructor because it is possible for application code that uses Relay as a
// library to construct a Config programmatically.
func ValidateConfig(c *Config, loggers ldlog.Loggers) error {
	var result ct.ValidationResult
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		result.AddError(nil, errors.New("TLS cert and key are required if TLS is enabled"))
	}

	for envName, envConfig := range c.Environment {
		if envConfig.SDKKey == "" {
			result.AddError(nil, errEnvironmentWithNoSDKKey(envName))
		}
	}

	if c.Redis.URL.IsDefined() {
		if c.Redis.Host != "" || c.Redis.Port.IsDefined() {
			result.AddError(nil, errors.New("please specify Redis URL or host/port, but not both"))
		}
	} else if c.Redis.Host != "" || c.Redis.Port.IsDefined() {
		host := c.Redis.Host
		if host == "" {
			host = defaultRedisHost
		}
		port := c.Redis.Port.GetOrElse(defaultRedisPort)
		url, err := ct.NewOptURLAbsoluteFromString(fmt.Sprintf("redis://%s:%d", host, port))
		if err != nil {
			result.AddError(nil, errors.New("invalid Redis hostname"))
		}
		c.Redis.URL = url
		c.Redis.Host = ""
		c.Redis.Port = ct.OptIntGreaterThanZero{}
	}

	databases := []string{}
	if c.Redis.Host != "" || c.Redis.URL.IsDefined() {
		databases = append(databases, "Redis")
	}
	if c.Consul.Host != "" {
		databases = append(databases, "Consul")
	}
	if c.DynamoDB.Enabled {
		databases = append(databases, "DynamoDB")
	}
	if len(databases) > 1 {
		result.AddError(nil, errMultipleDatabases(databases))
	}

	return result.GetError()
}
