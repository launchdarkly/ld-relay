package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

var (
	errObsoleteVariable = errors.New("this variable is no longer supported")
)

func errObsoleteVariableWithReplacement(preferredName string) error {
	return fmt.Errorf("this variable is no longer supported; use %s", preferredName)
}

// LoadConfigFromEnvironment sets parameters in a Config struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *Config, loggers ldlog.Loggers) error {
	result := LoadConfigFromEnvironmentBase(c, loggers)

	if !result.OK() {
		return result.GetError()
	}

	return ValidateConfig(c, loggers)
}

// LoadConfigFromEnvironmentBase performs the initial steps of reading Config fields from
// environment variables, but returns the intermediate result before fully validating it.
func LoadConfigFromEnvironmentBase(c *Config, loggers ldlog.Loggers) ct.ValidationResult {
	reader := ct.NewVarReaderFromEnvironment()

	reader.ReadStruct(&c.Main, false)

	reader.ReadStruct(&c.AutoConfig, false)

	reader.ReadStruct(&c.OfflineMode, false)

	// The following properties have the same environment variable names in AutoConfigConfig and in
	// OfflineModeConfig, because only one of those can be used at a time. We'll blank them out for
	// whichever section is not being used.
	if c.AutoConfig.Key.Defined() {
		c.OfflineMode.EnvAllowedOrigin = ct.OptStringList{}
		c.OfflineMode.EnvAllowedHeader = ct.OptStringList{}
		c.OfflineMode.EnvDatastorePrefix = ""
		c.OfflineMode.EnvDatastoreTableName = ""
	} else if c.OfflineMode.FileDataSource != "" {
		c.AutoConfig.EnvAllowedOrigin = ct.OptStringList{}
		c.AutoConfig.EnvAllowedHeader = ct.OptStringList{}
		c.AutoConfig.EnvDatastorePrefix = ""
		c.AutoConfig.EnvDatastoreTableName = ""
	}

	reader.ReadStruct(&c.Events, false)
	rejectObsoleteVariableName("EVENTS_SAMPLING_INTERVAL", "", reader)

	for envName, envKey := range reader.FindPrefixedValues("LD_ENV_") {
		var ec EnvConfig
		if c.Environment[envName] != nil {
			ec = *c.Environment[envName]
		}
		ec.SDKKey = SDKKey(envKey)
		subReader := reader.WithVarNameSuffix(envName)
		subReader.ReadStruct(&ec, false)
		rejectObsoleteVariableName("LD_TTL_MINUTES_"+envName, "LD_TTL_"+envName, reader)
		if c.Environment == nil {
			c.Environment = make(map[string]*EnvConfig)
		}
		c.Environment[envName] = &ec
	}

	for projKey := range reader.FindPrefixedValues("LD_FILTER_KEYS_") {
		var fc FiltersConfig
		if c.Filters[projKey] != nil {
			fc = *c.Filters[projKey]
		}
		subReader := reader.WithVarNameSuffix(projKey)
		subReader.ReadStruct(&fc, false)
		if c.Filters == nil {
			c.Filters = make(map[string]*FiltersConfig)
		}
		c.Filters[projKey] = &fc
	}

	useRedis := false
	reader.Read("USE_REDIS", &useRedis)
	if useRedis || c.Redis.Host != "" || c.Redis.URL.IsDefined() {
		portStr := ""
		if c.Redis.Port.IsDefined() {
			portStr = fmt.Sprintf("%d", c.Redis.Port.GetOrElse(0))
		}
		reader.ReadStruct(&c.Redis, false)
		reader.Read("REDIS_PORT", &portStr) // handled separately because it could be a string or a number

		if portStr != "" {
			if strings.HasPrefix(portStr, "tcp://") {
				// REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a Redis container
				hostAndPort := strings.TrimPrefix(portStr, "tcp://")
				fields := strings.Split(hostAndPort, ":")
				c.Redis.Host = fields[0]
				if len(fields) > 0 {
					if err := c.Redis.Port.UnmarshalText([]byte(fields[1])); err != nil {
						reader.AddError(ct.ValidationPath{"REDIS_PORT"}, err)
					}
				}
			} else {
				if c.Redis.Host == "" {
					c.Redis.Host = defaultRedisHost
				}
				reader.Read("REDIS_PORT", &c.Redis.Port)
			}
		}
		if !c.Redis.URL.IsDefined() && c.Redis.Host == "" && !c.Redis.Port.IsDefined() {
			// all they specified was USE_REDIS
			c.Redis.URL = defaultRedisURL
		}
		rejectObsoleteVariableName("REDIS_TTL", "CACHE_TTL", reader)
	}

	useConsul := false
	reader.Read("USE_CONSUL", &useConsul)
	if useConsul {
		c.Consul.Host = defaultConsulHost
		reader.ReadStruct(&c.Consul, false)
	}

	reader.Read("USE_DYNAMODB", &c.DynamoDB.Enabled)
	if c.DynamoDB.Enabled {
		reader.ReadStruct(&c.DynamoDB, false)
	}

	reader.ReadStruct(&c.MetricsConfig.Datadog, false)
	if c.MetricsConfig.Datadog.Enabled {
		for tagName, tagVal := range reader.FindPrefixedValues("DATADOG_TAG_") {
			c.MetricsConfig.Datadog.Tag = append(c.MetricsConfig.Datadog.Tag, tagName+":"+tagVal)
		}
		sort.Strings(c.MetricsConfig.Datadog.Tag) // for test determinacy
	}

	reader.ReadStruct(&c.MetricsConfig.Stackdriver, false)
	reader.ReadStruct(&c.MetricsConfig.Prometheus, false)

	reader.ReadStruct(&c.Proxy, false)

	return reader.Result()
}

func rejectObsoleteVariableName(oldName, preferredName string, reader *ct.VarReader) {
	// Unrecognized environment variables are normally ignored, but if someone has set a variable that
	// used to be used in configuration and is no longer used, we want to raise an error rather than just
	// silently omitting part of the configuration that they thought they had set.
	if os.Getenv(oldName) != "" {
		if preferredName == "" {
			reader.AddError(ct.ValidationPath{oldName}, errObsoleteVariable)
		} else {
			reader.AddError(ct.ValidationPath{oldName}, errObsoleteVariableWithReplacement(preferredName))
		}
	}
}
