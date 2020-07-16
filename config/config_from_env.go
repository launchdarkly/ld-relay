package config

import (
	"encoding"
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
func LoadConfigFromEnvironment(c *Config, loggers ldlog.Loggers) error {
	errs := make([]error, 0, 20)

	maybeSetFromEnvInt(&c.Main.Port, "PORT", &errs)
	maybeSetFromEnvAny(&c.Main.BaseURI, "BASE_URI", &errs)
	maybeSetFromEnvAny(&c.Main.StreamURI, "STREAM_URI", &errs)
	maybeSetFromEnvBool(&c.Main.ExitOnError, "EXIT_ON_ERROR")
	maybeSetFromEnvBool(&c.Main.ExitAlways, "EXIT_ALWAYS")
	maybeSetFromEnvBool(&c.Main.IgnoreConnectionErrors, "IGNORE_CONNECTION_ERRORS")
	maybeSetFromEnvAny(&c.Main.HeartbeatInterval, "HEARTBEAT_INTERVAL", &errs)
	maybeSetFromEnvAny(&c.Main.MaxClientConnectionTime, "MAX_CLIENT_CONNECTION_TIME", &errs)
	maybeSetFromEnvBool(&c.Main.TLSEnabled, "TLS_ENABLED")
	maybeSetFromEnv(&c.Main.TLSCert, "TLS_CERT")
	maybeSetFromEnv(&c.Main.TLSKey, "TLS_KEY")
	maybeSetFromEnvAny(&c.Main.LogLevel, "LOG_LEVEL", &errs)

	maybeSetFromEnvBool(&c.Events.SendEvents, "USE_EVENTS")
	maybeSetFromEnvAny(&c.Events.EventsURI, "EVENTS_HOST", &errs)
	maybeSetFromEnvAny(&c.Events.FlushInterval, "EVENTS_FLUSH_INTERVAL", &errs)
	maybeSetFromEnvInt32(&c.Events.SamplingInterval, "EVENTS_SAMPLING_INTERVAL", &errs)
	maybeSetFromEnvInt(&c.Events.Capacity, "EVENTS_CAPACITY", &errs)
	maybeSetFromEnvBool(&c.Events.InlineUsers, "EVENTS_INLINE_USERS")

	envNames, envKeys := getEnvVarsWithPrefix("LD_ENV_")
	for _, envName := range envNames {
		ec := EnvConfig{SDKKey: SDKKey(envKeys[envName])}
		ec.MobileKey = MobileKey(maybeEnvStr("LD_MOBILE_KEY_"+envName, string(ec.MobileKey)))
		ec.EnvID = EnvironmentID(maybeEnvStr("LD_CLIENT_SIDE_ID_"+envName, string(ec.EnvID)))
		maybeSetFromEnvBool(&ec.SecureMode, "LD_SECURE_MODE_"+envName)
		maybeSetFromEnv(&ec.Prefix, "LD_PREFIX_"+envName)
		maybeSetFromEnv(&ec.TableName, "LD_TABLE_NAME_"+envName)
		maybeSetFromEnvAny(&ec.TTL, "LD_TTL_"+envName, &errs)
		rejectObsoleteVariableName("LD_TTL_MINUTES_"+envName, "LD_TTL_"+envName, &errs)
		if s := os.Getenv("LD_ALLOWED_ORIGIN_" + envName); s != "" {
			ec.AllowedOrigin = strings.Split(s, ",")
		}
		maybeSetFromEnvAny(&ec.LogLevel, "LD_LOG_LEVEL_"+envName, &errs)
		// Not supported: EnvConfig.InsecureSkipVerify (that flag should only be used for testing, never in production)
		if c.Environment == nil {
			c.Environment = make(map[string]*EnvConfig)
		}
		c.Environment[envName] = &ec
	}

	useRedis := false
	maybeSetFromEnvBool(&useRedis, "USE_REDIS")
	if useRedis || c.Redis.Host != "" || c.Redis.URL.IsDefined() {
		portStr := ""
		if c.Redis.Port > 0 {
			portStr = fmt.Sprintf("%d", c.Redis.Port)
		}
		maybeSetFromEnvAny(&c.Redis.URL, "REDIS_URL", &errs)
		maybeSetFromEnv(&c.Redis.Host, "REDIS_HOST")
		maybeSetFromEnv(&portStr, "REDIS_PORT")

		if portStr != "" {
			if strings.HasPrefix(portStr, "tcp://") {
				// REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a Redis container
				hostAndPort := strings.TrimPrefix(portStr, "tcp://")
				fields := strings.Split(hostAndPort, ":")
				c.Redis.Host = fields[0]
				if len(fields) > 0 {
					setInt(&c.Redis.Port, "REDIS_PORT", fields[1], &errs)
				}
			} else {
				if c.Redis.Host == "" {
					c.Redis.Host = defaultRedisHost
				}
				setInt(&c.Redis.Port, "REDIS_PORT", portStr, &errs)
			}
		}
		if !c.Redis.URL.IsDefined() && c.Redis.Host == "" && c.Redis.Port == 0 {
			// all they specified was USE_REDIS
			c.Redis.URL = defaultRedisURL
		}
		maybeSetFromEnvBool(&c.Redis.TLS, "REDIS_TLS")
		maybeSetFromEnv(&c.Redis.Password, "REDIS_PASSWORD")
		maybeSetFromEnvAny(&c.Redis.LocalTTL, "CACHE_TTL", &errs)
		rejectObsoleteVariableName("REDIS_TTL", "CACHE_TTL", &errs)
	}

	useConsul := false
	maybeSetFromEnvBool(&useConsul, "USE_CONSUL")
	if useConsul {
		c.Consul.Host = defaultConsulHost
		maybeSetFromEnv(&c.Consul.Host, "CONSUL_HOST")
		maybeSetFromEnvAny(&c.Consul.LocalTTL, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.DynamoDB.Enabled, "USE_DYNAMODB")
	if c.DynamoDB.Enabled {
		maybeSetFromEnv(&c.DynamoDB.TableName, "DYNAMODB_TABLE")
		maybeSetFromEnvAny(&c.DynamoDB.URL, "DYNAMODB_URL", &errs)
		maybeSetFromEnvAny(&c.DynamoDB.LocalTTL, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Datadog.Enabled, "USE_DATADOG")
	if c.MetricsConfig.Datadog.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Datadog.Prefix, "DATADOG_PREFIX")
		maybeSetFromEnv(&c.MetricsConfig.Datadog.TraceAddr, "DATADOG_TRACE_ADDR")
		maybeSetFromEnv(&c.MetricsConfig.Datadog.StatsAddr, "DATADOG_STATS_ADDR")
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

	maybeSetFromEnvAny(&c.Proxy.URL, "PROXY_URL", &errs)
	maybeSetFromEnv(&c.Proxy.User, "PROXY_AUTH_USER")
	maybeSetFromEnv(&c.Proxy.Password, "PROXY_AUTH_PASSWORD")
	maybeSetFromEnv(&c.Proxy.Domain, "PROXY_AUTH_DOMAIN")
	maybeSetFromEnvBool(&c.Proxy.NTLMAuth, "PROXY_AUTH_NTLM")
	maybeSetFromEnv(&c.Proxy.CACertFiles, "PROXY_CA_CERTS")

	if len(errs) > 0 {
		ss := make([]string, 0, len(errs))
		for _, e := range errs {
			ss = append(ss, e.Error())
		}
		return errors.New(strings.Join(ss, ", "))
	}

	return ValidateConfig(c, loggers)
}

func rejectObsoleteVariableName(oldName, preferredName string, errs *[]error) {
	// Unrecognized environment variables are normally ignored, but if someone has set a variable that
	// used to be used in configuration and is no longer used, we want to raise an error rather than just
	// silently omitting part of the configuration that they thought they had set.
	if os.Getenv(oldName) != "" {
		*errs = append(*errs, fmt.Errorf("environment variable %s is no longer supported; use %s",
			oldName, preferredName))
	}
}

func maybeEnvStr(name string, defaultVal string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return defaultVal
}

func maybeSetFromEnv(prop *string, name string) bool {
	if s := os.Getenv(name); s != "" {
		*prop = s
		return true
	}
	return false
}

func maybeSetFromEnvAny(prop encoding.TextUnmarshaler, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		err := prop.UnmarshalText([]byte(s))
		if err != nil {
			*errs = append(*errs, fmt.Errorf("%s: %s", name, err.Error()))
		}
		return true
	}
	return false
}

func maybeSetFromEnvInt(prop *int, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		setInt(prop, name, s, errs)
		return true
	}
	return false
}

func setInt(prop *int, name string, value string, errs *[]error) {
	if n, err := strconv.Atoi(value); err != nil {
		*errs = append(*errs, fmt.Errorf("%s: must be an integer", name))
	} else {
		*prop = n
	}
}

func maybeSetFromEnvInt32(prop *int32, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		var n int64
		var err error
		if n, err = strconv.ParseInt(s, 10, 32); err != nil {
			*errs = append(*errs, fmt.Errorf("%s: must be an integer", name))
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
