package config

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

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
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		return errors.New("TLS cert and key are required if TLS is enabled")
	}

	for envName, envConfig := range c.Environment {
		if envConfig.APIKey != "" {
			if envConfig.SDKKey == "" {
				envConfig.SDKKey = SDKKey(envConfig.APIKey)
				c.Environment[envName] = envConfig
				loggers.Warn(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				loggers.Warn(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
	}

	if c.Redis.URL.IsDefined() {
		if c.Redis.Host != "" || c.Redis.Port != 0 {
			return errors.New("Please specify Redis URL or host/port, but not both")
		}
	} else if c.Redis.Host != "" || c.Redis.Port != 0 {
		host, port := c.Redis.Host, c.Redis.Port
		if host == "" {
			host = defaultRedisHost
		}
		if port <= 0 {
			port = defaultRedisPort
		}
		url, err := NewOptAbsoluteURLFromString(fmt.Sprintf("redis://%s:%d", host, port))
		if err != nil {
			return err
		}
		c.Redis.URL = url
		c.Redis.Host = ""
		c.Redis.Port = 0
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
		return fmt.Errorf("Multiple databases are enabled (%s); only one is allowed",
			strings.Join(databases, ", "))
	}

	return nil
}
