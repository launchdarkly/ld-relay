package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// LoadConfigFromEnvironment sets parameters in a Config struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *Config) error {
	errs := make([]error, 0, 20)

	maybeSetFromEnvInt(&c.Main.Port, "PORT", &errs)
	maybeSetFromEnv(&c.Main.BaseUri, "BASE_URI")
	maybeSetFromEnv(&c.Main.StreamUri, "STREAM_URI")
	maybeSetFromEnvBool(&c.Main.ExitOnError, "EXIT_ON_ERROR")
	maybeSetFromEnvBool(&c.Main.ExitAlways, "EXIT_ALWAYS")
	maybeSetFromEnvBool(&c.Main.IgnoreConnectionErrors, "IGNORE_CONNECTION_ERRORS")
	maybeSetFromEnvInt(&c.Main.HeartbeatIntervalSecs, "HEARTBEAT_INTERVAL", &errs)
	maybeSetFromEnvBool(&c.Main.TLSEnabled, "TLS_ENABLED")
	maybeSetFromEnv(&c.Main.TLSCert, "TLS_CERT")
	maybeSetFromEnv(&c.Main.TLSKey, "TLS_KEY")
	maybeSetFromEnv(&c.Main.LogLevel, "LOG_LEVEL")

	maybeSetFromEnvBool(&c.Events.SendEvents, "USE_EVENTS")
	maybeSetFromEnv(&c.Events.EventsUri, "EVENTS_HOST")
	maybeSetFromEnvInt(&c.Events.FlushIntervalSecs, "EVENTS_FLUSH_INTERVAL", &errs)
	maybeSetFromEnvInt32(&c.Events.SamplingInterval, "EVENTS_SAMPLING_INTERVAL", &errs)
	maybeSetFromEnvInt(&c.Events.Capacity, "EVENTS_CAPACITY", &errs)
	maybeSetFromEnvBool(&c.Events.InlineUsers, "EVENTS_INLINE_USERS")

	envNames, envKeys := getEnvVarsWithPrefix("LD_ENV_")
	for _, envName := range envNames {
		ec := EnvConfig{SdkKey: envKeys[envName]}
		ec.MobileKey = maybeEnvStrPtr("LD_MOBILE_KEY_" + envName)
		ec.EnvId = maybeEnvStrPtr("LD_CLIENT_SIDE_ID_" + envName)
		maybeSetFromEnv(&ec.Prefix, "LD_PREFIX_"+envName)
		maybeSetFromEnv(&ec.TableName, "LD_TABLE_NAME_"+envName)
		maybeSetFromEnvInt(&ec.TtlMinutes, "LD_TTL_MINUTES_"+envName, &errs)
		if s := os.Getenv("LD_ALLOWED_ORIGIN_" + envName); s != "" {
			values := strings.Split(s, ",")
			ec.AllowedOrigin = &values
		}
		maybeSetFromEnv(&ec.LogLevel, "LD_LOG_LEVEL_"+envName)
		// Not supported: EnvConfig.InsecureSkipVerify (that flag should only be used for testing, never in production)
		if c.Environment == nil {
			c.Environment = make(map[string]*EnvConfig)
		}
		c.Environment[envName] = &ec
	}

	useRedis := false
	maybeSetFromEnvBool(&useRedis, "USE_REDIS")
	if useRedis || (c.Redis.Host != "" || c.Redis.Url != "") {
		host := c.Redis.Host
		portStr := ""
		if c.Redis.Port > 0 {
			portStr = fmt.Sprintf("%d", c.Redis.Port)
		}
		url := c.Redis.Url
		maybeSetFromEnv(&host, "REDIS_HOST")
		maybeSetFromEnv(&portStr, "REDIS_PORT")
		maybeSetFromEnv(&url, "REDIS_URL")
		if (host != "" || portStr != "") && url != "" {
			return errors.New("Please specify REDIS_HOST or REDIS_URL")
		}
		if url != "" {
			c.Redis.Url = url
			c.Redis.Host = ""
		} else if host != "" || portStr != "" {
			if strings.HasPrefix(portStr, "tcp://") {
				// REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a Redis container
				hostAndPort := strings.TrimPrefix(portStr, "tcp://")
				fields := strings.Split(hostAndPort, ":")
				c.Redis.Host = fields[0]
				if len(fields) > 0 {
					c.Redis.Port, _ = strconv.Atoi(fields[1])
				}
			} else {
				c.Redis.Host = host
				c.Redis.Port = defaultRedisPort
				maybeSetFromEnvInt(&c.Redis.Port, "REDIS_PORT", &errs)
				c.Redis.Url = ""
			}
		} else {
			c.Redis.Host = "localhost"
			c.Redis.Port = defaultRedisPort
		}
		maybeSetFromEnvBool(&c.Redis.Tls, "REDIS_TLS")
		maybeSetFromEnv(&c.Redis.Password, "REDIS_PASSWORD")
		maybeSetFromEnvInt(&c.Redis.LocalTtl, "REDIS_TTL", &errs)
		maybeSetFromEnvInt(&c.Redis.LocalTtl, "CACHE_TTL", &errs) // synonym
	}

	useConsul := false
	maybeSetFromEnvBool(&useConsul, "USE_CONSUL")
	if useConsul {
		c.Consul.Host = "localhost"
		maybeSetFromEnv(&c.Consul.Host, "CONSUL_HOST")
		maybeSetFromEnvInt(&c.Consul.LocalTtl, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.DynamoDB.Enabled, "USE_DYNAMODB")
	if c.DynamoDB.Enabled {
		maybeSetFromEnv(&c.DynamoDB.TableName, "DYNAMODB_TABLE")
		maybeSetFromEnv(&c.DynamoDB.Url, "DYNAMODB_URL")
		maybeSetFromEnvInt(&c.DynamoDB.LocalTtl, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Datadog.Enabled, "USE_DATADOG")
	if c.MetricsConfig.Datadog.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Datadog.Prefix, "DATADOG_PREFIX")
		c.MetricsConfig.Datadog.TraceAddr = maybeEnvStrPtr("DATADOG_TRACE_ADDR")
		c.MetricsConfig.Datadog.StatsAddr = maybeEnvStrPtr("DATADOG_STATS_ADDR")
		tagNames, tagVals := getEnvVarsWithPrefix("DATADOG_TAG_")
		for _, tagName := range tagNames {
			c.MetricsConfig.Datadog.Tag = append(c.MetricsConfig.Datadog.Tag, tagName+":"+tagVals[tagName])
		}
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Stackdriver.Enabled, "USE_STACKDRIVER")
	if c.MetricsConfig.Stackdriver.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Stackdriver.Prefix, "STACKDRIVER_PREFIX")
		maybeSetFromEnv(&c.MetricsConfig.Stackdriver.ProjectID, "STACKDRIVER_PROJECT_ID")
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Prometheus.Enabled, "USE_PROMETHEUS")
	if c.MetricsConfig.Prometheus.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Prometheus.Prefix, "PROMETHEUS_PREFIX")
		maybeSetFromEnvInt(&c.MetricsConfig.Prometheus.Port, "PROMETHEUS_PORT", &errs)
	}

	maybeSetFromEnv(&c.Proxy.Url, "PROXY_URL")
	maybeSetFromEnv(&c.Proxy.User, "PROXY_AUTH_USER")
	maybeSetFromEnv(&c.Proxy.Password, "PROXY_AUTH_PASSWORD")
	maybeSetFromEnv(&c.Proxy.Domain, "PROXY_AUTH_DOMAIN")
	maybeSetFromEnvBool(&c.Proxy.NtlmAuth, "PROXY_AUTH_NTLM")
	maybeSetFromEnv(&c.Proxy.CaCertFiles, "PROXY_CA_CERTS")

	if len(errs) > 0 {
		ss := make([]string, 0, len(errs))
		for _, e := range errs {
			ss = append(ss, e.Error())
		}
		return errors.New(strings.Join(ss, ", "))
	}

	return ValidateConfig(c)
}

func getLogLevelByName(levelName string) (ldlog.LogLevel, bool) {
	if levelName == "" {
		return ldlog.Info, true
	}
	for _, level := range []ldlog.LogLevel{ldlog.Debug, ldlog.Info, ldlog.Warn, ldlog.Error, ldlog.None} {
		if strings.EqualFold(level.Name(), levelName) {
			return level, true
		}
	}
	return 0, false
}

func maybeEnvStrPtr(name string) *string {
	if s := os.Getenv(name); s != "" {
		return &s
	}
	return nil
}

func maybeSetFromEnv(prop *string, name string) bool {
	if s := os.Getenv(name); s != "" {
		*prop = s
		return true
	}
	return false
}

func maybeSetFromEnvInt(prop *int, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		n := 0
		var err error
		if n, err = strconv.Atoi(s); err != nil {
			*errs = append(*errs, fmt.Errorf("%s must be an integer", name))
		} else {
			*prop = n
		}
		return true
	}
	return false
}

func maybeSetFromEnvInt32(prop *int32, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		var n int64
		var err error
		if n, err = strconv.ParseInt(s, 10, 32); err != nil {
			*errs = append(*errs, fmt.Errorf("%s must be an integer", name))
		} else {
			*prop = int32(n)
		}
		return true
	}
	return false
}

func maybeSetFromEnvBool(prop *bool, name string) bool {
	if s, found := os.LookupEnv(name); found {
		if s == "1" || s == "true" {
			*prop = true
		} else {
			*prop = false
		}
		return true
	}
	return false
}

func getEnvVarsWithPrefix(prefix string) ([]string, map[string]string) {
	names := []string{}
	values := make(map[string]string)
	allVars := os.Environ()
	sort.Strings(allVars)
	for _, e := range allVars {
		if strings.HasPrefix(e, prefix) {
			fields := strings.Split(e, "=")
			if len(fields) == 2 {
				strippedName := strings.TrimPrefix(fields[0], prefix)
				names = append(names, strippedName)
				values[strippedName] = fields[1]
			}
		}
	}
	return names, values
}
