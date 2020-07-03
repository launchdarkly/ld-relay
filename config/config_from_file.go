package config

import (
	"errors"
	"fmt"
	"strings"

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

	for envName, envConfig := range c.Environment {
		if envConfig.ApiKey != "" {
			if envConfig.SdkKey == "" {
				envConfig.SdkKey = envConfig.ApiKey
				c.Environment[envName] = envConfig
				loggers.Warn(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				loggers.Warn(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
	}

	return ValidateConfig(c)
}

// ValidateConfig ensures that the configuration does not contain contradictory properties.
func ValidateConfig(c *Config) error {
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		return errors.New("TLS cert and key are required if TLS is enabled")
	}
	if _, ok := getLogLevelByName(c.Main.LogLevel); !ok {
		return fmt.Errorf(`Invalid log level "%s"`, c.Main.LogLevel)
	}
	for _, ec := range c.Environment {
		if _, ok := getLogLevelByName(ec.LogLevel); !ok {
			return fmt.Errorf(`Invalid environment log level "%s"`, ec.LogLevel)
		}
	}
	databases := []string{}
	if c.Redis.Host != "" || c.Redis.Url != "" {
		databases = append(databases, "Redis")
	}
	if c.Consul.Host != "" {
		databases = append(databases, "Consul")
	}
	if c.DynamoDB.Enabled {
		databases = append(databases, "DynamoDB")
	}
	if len(databases) > 1 {
		return fmt.Errorf("Multiple databases are enabled (%s); only one is allowed",
			strings.Join(databases, ", "))
	}
	return nil
}
