package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// LoadConfigFromEnvironment sets parameters in a Config struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *Config, loggers ldlog.Loggers) error {
	reader := ct.NewVarReaderFromEnvironment()

	reader.ReadStruct(&c.Main, false)
	reader.ReadStruct(&c.Events, false)

	maybeSetFromEnvInt32(&c.Events.SamplingInterval, "EVENTS_SAMPLING_INTERVAL", reader)

	for envName, envKey := range reader.FindPrefixedValues("LD_ENV_") {
		ec := EnvConfig{SDKKey: SDKKey(envKey)}
		subReader := reader.WithVarNameSuffix(envName)
		subReader.ReadStruct(&ec, false)
		rejectObsoleteVariableName("LD_TTL_MINUTES_"+envName, "LD_TTL_"+envName, reader)
		// Not supported: EnvConfig.InsecureSkipVerify (that flag should only be used for testing, never in production)
		if c.Environment == nil {
			c.Environment = make(map[string]*EnvConfig)
		}
		c.Environment[envName] = &ec
	}

	useRedis := false
	reader.Read("USE_REDIS", &useRedis)
	if useRedis || c.Redis.Host != "" || c.Redis.URL.IsDefined() {
		portStr := ""
		if c.Redis.Port > 0 {
			portStr = fmt.Sprintf("%d", c.Redis.Port)
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
					setInt(&c.Redis.Port, "REDIS_PORT", fields[1], reader)
				}
			} else {
				if c.Redis.Host == "" {
					c.Redis.Host = defaultRedisHost
				}
				reader.Read("REDIS_PORT", &c.Redis.Port)
			}
		}
		if !c.Redis.URL.IsDefined() && c.Redis.Host == "" && c.Redis.Port == 0 {
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

	reader.Read("USE_DATADOG", &c.MetricsConfig.Datadog.Enabled)
	if c.MetricsConfig.Datadog.Enabled {
		reader.Read("DATADOG_PREFIX", &c.MetricsConfig.Datadog.Prefix)
		reader.ReadStruct(&c.MetricsConfig.Datadog, false)
		for tagName, tagVal := range reader.FindPrefixedValues("DATADOG_TAG_") {
			c.MetricsConfig.Datadog.Tag = append(c.MetricsConfig.Datadog.Tag, tagName+":"+tagVal)
		}
		sort.Strings(c.MetricsConfig.Datadog.Tag) // for test determinacy
	}

	reader.Read("USE_STACKDRIVER", &c.MetricsConfig.Stackdriver.Enabled)
	if c.MetricsConfig.Stackdriver.Enabled {
		reader.ReadStruct(&c.MetricsConfig.Stackdriver, false)
		reader.Read("STACKDRIVER_PREFIX", &c.MetricsConfig.Stackdriver.Prefix)
	}

	reader.Read("USE_PROMETHEUS", &c.MetricsConfig.Prometheus.Enabled)
	if c.MetricsConfig.Prometheus.Enabled {
		reader.ReadStruct(&c.MetricsConfig.Prometheus, false)
		reader.Read("PROMETHEUS_PREFIX", &c.MetricsConfig.Prometheus.Prefix)
	}

	reader.ReadStruct(&c.Proxy, false)

	if !reader.Result().OK() {
		return reader.Result().GetError()
	}

	return ValidateConfig(c, loggers)
}

func rejectObsoleteVariableName(oldName, preferredName string, reader *ct.VarReader) {
	// Unrecognized environment variables are normally ignored, but if someone has set a variable that
	// used to be used in configuration and is no longer used, we want to raise an error rather than just
	// silently omitting part of the configuration that they thought they had set.
	if os.Getenv(oldName) != "" {
		reader.AddError(ct.ValidationPath{oldName},
			fmt.Errorf("this variable is no longer supported; use %s", preferredName))
	}
}

func setInt(prop *int, name string, value string, reader *ct.VarReader) {
	if n, err := strconv.Atoi(value); err != nil {
		reader.AddError(ct.ValidationPath{name}, errors.New("not a valid integer"))
	} else {
		*prop = n
	}
}

func maybeSetFromEnvInt32(prop *int32, name string, reader *ct.VarReader) {
	var n int
	if reader.Read(name, &n) {
		*prop = int32(n)
	}
}
