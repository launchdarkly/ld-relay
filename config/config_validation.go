package config

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// ValidateConfig ensures that the configuration does not contain contradictory properties.
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
			envConfig.APIKey = "" // to avoid confusion if any other code sees this
		}
	}

	if c.Redis.URL.IsDefined() {
		if c.Redis.Host != "" || c.Redis.Port != 0 {
			return errors.New("please specify Redis URL or host/port, but not both")
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
			return errors.New("invalid Redis hostname")
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
		return fmt.Errorf("multiple databases are enabled (%s); only one is allowed",
			strings.Join(databases, ", "))
	}

	return nil
}
