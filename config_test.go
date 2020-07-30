package relay

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/launchdarkly/ld-relay.v5/httpconfig"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
)

func TestConfigFromEnvironmentWithAllBaseProperties(t *testing.T) {
	testValidConfigVars(t,
		func(c *Config) {
			c.Main = MainConfig{
				Port:                   8333,
				BaseUri:                "http://base",
				StreamUri:              "http://stream",
				ExitOnError:            true,
				ExitAlways:             true,
				IgnoreConnectionErrors: true,
				HeartbeatIntervalSecs:  90,
				TLSEnabled:             true,
				TLSCert:                "cert",
				TLSKey:                 "key",
				LogLevel:               "warn",
			}
			c.Events = events.Config{
				SendEvents:        true,
				EventsUri:         "http://events",
				FlushIntervalSecs: 120,
				SamplingInterval:  3,
				Capacity:          500,
				InlineUsers:       true,
			}
			origins := []string{"https://oa", "https://rann"}
			c.Environment = map[string]*EnvConfig{
				"earth": &EnvConfig{
					SdkKey:    "earth-sdk",
					MobileKey: strPtr("earth-mob"),
					EnvId:     strPtr("earth-env"),
					Prefix:    "earth-",
					TableName: "earth-table",
					LogLevel:  "debug",
				},
				"krypton": &EnvConfig{
					SdkKey:        "krypton-sdk",
					MobileKey:     strPtr("krypton-mob"),
					EnvId:         strPtr("krypton-env"),
					Prefix:        "krypton-",
					TableName:     "krypton-table",
					AllowedOrigin: &origins,
					TtlMinutes:    5,
				},
			}
		},
		map[string]string{
			"PORT":                      "8333",
			"BASE_URI":                  "http://base",
			"STREAM_URI":                "http://stream",
			"EXIT_ON_ERROR":             "1",
			"EXIT_ALWAYS":               "1",
			"IGNORE_CONNECTION_ERRORS":  "1",
			"HEARTBEAT_INTERVAL":        "90",
			"TLS_ENABLED":               "1",
			"TLS_CERT":                  "cert",
			"TLS_KEY":                   "key",
			"LOG_LEVEL":                 "warn",
			"USE_EVENTS":                "1",
			"EVENTS_HOST":               "http://events",
			"EVENTS_FLUSH_INTERVAL":     "120",
			"EVENTS_SAMPLING_INTERVAL":  "3",
			"EVENTS_CAPACITY":           "500",
			"EVENTS_INLINE_USERS":       "1",
			"LD_ENV_earth":              "earth-sdk",
			"LD_MOBILE_KEY_earth":       "earth-mob",
			"LD_CLIENT_SIDE_ID_earth":   "earth-env",
			"LD_PREFIX_earth":           "earth-",
			"LD_TABLE_NAME_earth":       "earth-table",
			"LD_LOG_LEVEL_earth":        "debug",
			"LD_ENV_krypton":            "krypton-sdk",
			"LD_MOBILE_KEY_krypton":     "krypton-mob",
			"LD_CLIENT_SIDE_ID_krypton": "krypton-env",
			"LD_PREFIX_krypton":         "krypton-",
			"LD_TABLE_NAME_krypton":     "krypton-table",
			"LD_ALLOWED_ORIGIN_krypton": "https://oa,https://rann",
			"LD_TTL_MINUTES_krypton":    "5",
		},
	)
}

func TestConfigFromEnvironmentTreatsTrueAsTrue(t *testing.T) {
	testValidConfigVars(t,
		func(c *Config) {
			c.Main.ExitOnError = true
		},
		map[string]string{"EXIT_ON_ERROR": "true"},
	)
}

func TestConfigFromEnvironmentTreatsAnyValueOtherThan1OrTrueAsFalse(t *testing.T) {
	testValidConfigVars(t,
		func(c *Config) {
			c.Main.ExitOnError = false
		},
		map[string]string{"EXIT_ON_ERROR": "not really"},
	)
}

func TestConfigFromEnvironmentWithInvalidInt(t *testing.T) {
	testInvalidConfigVars(t,
		map[string]string{"PORT": "not-numeric"},
		"PORT must be an integer",
	)
}

func TestConfigFromEnvironmentTLSValidation(t *testing.T) {
	testInvalidConfigVars(t,
		map[string]string{"TLS_ENABLED": "1"},
		"TLS cert and key are required if TLS is enabled",
	)
	testInvalidConfigVars(t,
		map[string]string{"TLS_ENABLED": "1", "TLS_CERT": "cert"},
		"TLS cert and key are required if TLS is enabled",
	)
	testInvalidConfigVars(t,
		map[string]string{"TLS_ENABLED": "1", "TLS_KEY": "key"},
		"TLS cert and key are required if TLS is enabled",
	)
	testInvalidConfigVars(t,
		map[string]string{"TLS_ENABLED": "1", "TLS_CERT": "cert", "TLS_KEY": "key", "TLS_MIN_VERSION": "1"},
		"invalid minimum TLS version",
	)
}

func TestConfigFromEnvironmentWithRedis(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Redis = RedisConfig{
				Host:     "localhost",
				Port:     6379,
				LocalTtl: defaultDatabaseLocalTTLMs,
				Tls:      false,
				Password: "",
			}
		},
		map[string]string{
			"USE_REDIS": "1",
		},
	)
	// test setting host and port plus all optional parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Redis = RedisConfig{
				Host:     "redishost",
				Port:     6400,
				LocalTtl: 3000,
				Tls:      true,
				Password: "pass",
			}
		},
		map[string]string{
			"USE_REDIS":      "1",
			"REDIS_HOST":     "redishost",
			"REDIS_PORT":     "6400",
			"REDIS_TLS":      "1",
			"REDIS_PASSWORD": "pass",
			"CACHE_TTL":      "3000",
		},
	)
	// test setting obsolete REDIS_TTL
	testValidConfigVars(t,
		func(c *Config) {
			c.Redis = RedisConfig{
				Host:     "localhost",
				Port:     6379,
				LocalTtl: 3000,
			}
		},
		map[string]string{
			"USE_REDIS": "1",
			"REDIS_TTL": "3000",
		},
	)
	// test setting URL instead of host/port
	testValidConfigVars(t,
		func(c *Config) {
			c.Redis = RedisConfig{
				Url:      "http://redishost:6400",
				LocalTtl: defaultDatabaseLocalTTLMs,
			}
		},
		map[string]string{
			"USE_REDIS": "1",
			"REDIS_URL": "http://redishost:6400",
		},
	)
	// test special Docker syntax
	testValidConfigVars(t,
		func(c *Config) {
			c.Redis = RedisConfig{
				Host:     "redishost",
				Port:     6400,
				LocalTtl: defaultDatabaseLocalTTLMs,
			}
		},
		map[string]string{
			"USE_REDIS":  "1",
			"REDIS_PORT": "tcp://redishost:6400",
		},
	)
	// error for conflicting parameters
	testInvalidConfigVars(t,
		map[string]string{
			"USE_REDIS":  "1",
			"REDIS_HOST": "redishost",
			"REDIS_URL":  "http://redishost:6400",
		},
		"Please specify REDIS_HOST or REDIS_URL",
	)
}

func TestConfigFromEnvironmentWithConsul(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Consul = ConsulConfig{
				Host:     "localhost",
				LocalTtl: defaultDatabaseLocalTTLMs,
			}
		},
		map[string]string{
			"USE_CONSUL": "1",
		},
	)
	// test setting all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Consul = ConsulConfig{
				Host:     "consulhost",
				LocalTtl: 3000,
			}
		},
		map[string]string{
			"USE_CONSUL":  "1",
			"CONSUL_HOST": "consulhost",
			"CACHE_TTL":   "3000",
		},
	)
}

func TestConfigFromEnvironmentWithDynamoDB(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.DynamoDB = DynamoDBConfig{
				Enabled:  true,
				LocalTtl: defaultDatabaseLocalTTLMs,
			}
		},
		map[string]string{
			"USE_DYNAMODB": "1",
		},
	)
	// test setting all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.DynamoDB = DynamoDBConfig{
				Enabled:   true,
				TableName: "table",
				Url:       "http://localhost:8000",
				LocalTtl:  3000,
			}
		},
		map[string]string{
			"USE_DYNAMODB":   "1",
			"DYNAMODB_TABLE": "table",
			"DYNAMODB_URL":   "http://localhost:8000",
			"CACHE_TTL":      "3000",
		},
	)
}

func TestConfigFromEnvironmentDisallowsMultipleDatabases(t *testing.T) {
	testInvalidConfigVars(t,
		map[string]string{
			"USE_REDIS":    "1",
			"USE_CONSUL":   "1",
			"USE_DYNAMODB": "1",
		},
		"Multiple databases are enabled (Redis, Consul, DynamoDB); only one is allowed",
	)
}

func TestConfigFromEnvironmentWithDatadogConfig(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Datadog = DatadogConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
				TraceAddr:           nil,
				StatsAddr:           nil,
				Tag:                 nil,
			}
		},
		map[string]string{
			"USE_DATADOG": "1",
		},
	)
	// test setting all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Datadog = DatadogConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
				TraceAddr:           strPtr("trace"),
				StatsAddr:           strPtr("stats"),
				Tag:                 []string{"tag1:value1", "tag2:value2"},
			}
		},
		map[string]string{
			"USE_DATADOG":        "1",
			"DATADOG_PREFIX":     "pre-",
			"DATADOG_TRACE_ADDR": "trace",
			"DATADOG_STATS_ADDR": "stats",
			"DATADOG_TAG_tag1":   "value1",
			"DATADOG_TAG_tag2":   "value2",
		},
	)
}

func TestConfigFromEnvironmentWithStackdriverConfig(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Stackdriver = StackdriverConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
				ProjectID:           "",
			}
		},
		map[string]string{
			"USE_STACKDRIVER": "1",
		},
	)
	// test setting all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Stackdriver = StackdriverConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
				ProjectID:           "proj",
			}
		},
		map[string]string{
			"USE_STACKDRIVER":        "1",
			"STACKDRIVER_PREFIX":     "pre-",
			"STACKDRIVER_PROJECT_ID": "proj",
		},
	)
}

func TestConfigFromEnvironmentWithPrometheusConfig(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Prometheus = PrometheusConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
				Port:                8031,
			}
		},
		map[string]string{
			"USE_PROMETHEUS": "1",
		},
	)
	// test setting all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Prometheus = PrometheusConfig{
				CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
				Port:                8333,
			}
		},
		map[string]string{
			"USE_PROMETHEUS":    "1",
			"PROMETHEUS_PREFIX": "pre-",
			"PROMETHEUS_PORT":   "8333",
		},
	)
}

func TestConfigFromEnvironmentWithProxyConfig(t *testing.T) {
	// test defaults for all parameters
	testValidConfigVars(t,
		func(c *Config) {
			c.Proxy = httpconfig.ProxyConfig{
				Url:         "http://proxy",
				User:        "user",
				Password:    "pass",
				Domain:      "domain",
				NtlmAuth:    true,
				CaCertFiles: "cert",
			}
		},
		// test setting all parameters
		map[string]string{
			"PROXY_URL":           "http://proxy",
			"PROXY_AUTH_USER":     "user",
			"PROXY_AUTH_PASSWORD": "pass",
			"PROXY_AUTH_DOMAIN":   "domain",
			"PROXY_AUTH_NTLM":     "1",
			"PROXY_CA_CERTS":      "cert",
		},
	)
}

func testValidConfigVars(t *testing.T, buildConfig func(c *Config), vars map[string]string) {
	oldVars := captureEnvironment()
	defer setEnvironment(oldVars)
	setEnvironment(vars)

	expectedConfig := DefaultConfig
	buildConfig(&expectedConfig)

	c := DefaultConfig
	err := LoadConfigFromEnvironment(&c)
	require.NoError(t, err)

	assert.Equal(t, expectedConfig, c)
}

func testInvalidConfigVars(t *testing.T, vars map[string]string, errMessage string) {
	oldVars := captureEnvironment()
	defer setEnvironment(oldVars)
	setEnvironment(vars)

	c := DefaultConfig
	err := LoadConfigFromEnvironment(&c)
	require.Error(t, err)
	assert.Equal(t, errMessage, err.Error())
}

func captureEnvironment() map[string]string {
	ret := make(map[string]string)
	for _, name := range os.Environ() {
		ret[name] = os.Getenv(name)
	}
	return ret
}

func setEnvironment(vars map[string]string) {
	os.Clearenv()
	for name, value := range vars {
		os.Setenv(name, value)
	}
}

func strPtr(s string) *string {
	return &s
}
