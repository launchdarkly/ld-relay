package config

import (
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type testDataValidConfig struct {
	name        string
	makeConfig  func(c *Config)
	envVars     map[string]string
	fileContent string
}

type testDataInvalidConfig struct {
	name         string
	envVarsError string
	fileError    string
	envVars      map[string]string
	fileContent  string
}

func makeValidConfigs() []testDataValidConfig {
	return []testDataValidConfig{
		makeValidConfigAllBaseProperties(),
		makeValidConfigRedisMinimal(),
		makeValidConfigRedisAll(),
		makeValidConfigRedisURL(),
		makeValidConfigRedisPortOnly(),
		makeValidConfigRedisDockerPort(),
		makeValidConfigConsulMinimal(),
		makeValidConfigConsulAll(),
		makeValidConfigDynamoDBMinimal(),
		makeValidConfigDynamoDBAll(),
		makeValidConfigDatadogMinimal(),
		makeValidConfigDatadogAll(),
		makeValidConfigStackdriverMinimal(),
		makeValidConfigStackdriverAll(),
		makeValidConfigPrometheusMinimal(),
		makeValidConfigPrometheusAll(),
		makeValidConfigProxy(),
	}
}

func makeInvalidConfigs() []testDataInvalidConfig {
	return []testDataInvalidConfig{
		makeInvalidConfigTLSWithNoCertOrKey(),
		makeInvalidConfigTLSWithNoCert(),
		makeInvalidConfigTLSWithNoKey(),
		makeInvalidConfigRedisInvalidHostname(),
		makeInvalidConfigRedisConflictingParams(),
		makeInvalidConfigMultipleDatabases(),
	}
}

func makeValidConfigAllBaseProperties() testDataValidConfig {
	c := testDataValidConfig{name: "all base properties"}
	c.makeConfig = func(c *Config) {
		c.Main = MainConfig{
			Port:                   8333,
			BaseURI:                newOptAbsoluteURLMustBeValid("http://base"),
			StreamURI:              newOptAbsoluteURLMustBeValid("http://stream"),
			ExitOnError:            true,
			ExitAlways:             true,
			IgnoreConnectionErrors: true,
			HeartbeatInterval:      NewOptDuration(90 * time.Second),
			TLSEnabled:             true,
			TLSCert:                "cert",
			TLSKey:                 "key",
			LogLevel:               NewOptLogLevel(ldlog.Warn),
		}
		c.Events = EventsConfig{
			SendEvents:       true,
			EventsURI:        newOptAbsoluteURLMustBeValid("http://events"),
			FlushInterval:    NewOptDuration(120 * time.Second),
			SamplingInterval: 3,
			Capacity:         500,
			InlineUsers:      true,
		}
		c.Environment = map[string]*EnvConfig{
			"earth": &EnvConfig{
				SDKKey:    "earth-sdk",
				MobileKey: "earth-mob",
				EnvID:     "earth-env",
				Prefix:    "earth-",
				TableName: "earth-table",
				LogLevel:  NewOptLogLevel(ldlog.Debug),
			},
			"krypton": &EnvConfig{
				SDKKey:        "krypton-sdk",
				MobileKey:     "krypton-mob",
				EnvID:         "krypton-env",
				Prefix:        "krypton-",
				TableName:     "krypton-table",
				AllowedOrigin: []string{"https://oa", "https://rann"},
				TTL:           NewOptDuration(5 * time.Minute),
			},
		}
	}
	c.envVars = map[string]string{
		"PORT":                      "8333",
		"BASE_URI":                  "http://base",
		"STREAM_URI":                "http://stream",
		"EXIT_ON_ERROR":             "1",
		"EXIT_ALWAYS":               "1",
		"IGNORE_CONNECTION_ERRORS":  "1",
		"HEARTBEAT_INTERVAL":        "90s",
		"TLS_ENABLED":               "1",
		"TLS_CERT":                  "cert",
		"TLS_KEY":                   "key",
		"LOG_LEVEL":                 "warn",
		"USE_EVENTS":                "1",
		"EVENTS_HOST":               "http://events",
		"EVENTS_FLUSH_INTERVAL":     "120s",
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
		"LD_TTL_krypton":            "5m",
	}
	c.fileContent = `
[Main]
Port = 8333
BaseUri = "http://base"
StreamUri = "http://stream"
ExitOnError = 1
ExitAlways = 1
IgnoreConnectionErrors = 1
HeartbeatInterval = 90s
TLSEnabled = 1
TLSCert = "cert"
TLSKey = "key"
LogLevel = "warn"

[Events]
SendEvents = 1
EventsUri = "http://events"
FlushInterval = 120s
SamplingInterval = 3
Capacity = 500
InlineUsers = 1

[Environment "earth"]
SdkKey = "earth-sdk"
MobileKey = "earth-mob"
EnvId = "earth-env"
Prefix = "earth-"
TableName = "earth-table"
LogLevel = "debug"

[Environment "krypton"]
SdkKey = "krypton-sdk"
MobileKey = "krypton-mob"
EnvId = "krypton-env"
Prefix = "krypton-"
TableName = "krypton-table"
AllowedOrigin = "https://oa"
AllowedOrigin = "https://rann"
TTL = 5m
`
	return c
}

func makeValidConfigRedisMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptAbsoluteURLMustBeValid("redis://localhost:6379"),
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS": "1",
	}
	c.fileContent = `
[Redis]
Host = "localhost"
Port = 6379
`
	return c
}

func makeValidConfigRedisAll() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - all parameters"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL:      newOptAbsoluteURLMustBeValid("redis://redishost:6400"),
			LocalTTL: NewOptDuration(3 * time.Second),
			TLS:      true,
			Password: "pass",
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS":      "1",
		"REDIS_HOST":     "redishost",
		"REDIS_PORT":     "6400",
		"REDIS_TLS":      "1",
		"REDIS_PASSWORD": "pass",
		"CACHE_TTL":      "3s",
	}
	c.fileContent = `
[Redis]
Host = "redishost"
Port = 6400
TLS = 1
Password = "pass"
LocalTTL = 3s
`
	return c
}

func makeValidConfigRedisURL() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - URL instead of host/port"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptAbsoluteURLMustBeValid("rediss://redishost:6400"),
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS": "1",
		"REDIS_URL": "rediss://redishost:6400",
	}
	c.fileContent = `
[Redis]
Url = "rediss://redishost:6400"
`
	return c
}

func makeValidConfigRedisPortOnly() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - URL instead of host/port"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptAbsoluteURLMustBeValid("redis://localhost:9999"),
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_PORT": "9999",
	}
	c.fileContent = `
[Redis]
Port = 9999
`
	return c
}

func makeValidConfigRedisDockerPort() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - special Docker port syntax"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptAbsoluteURLMustBeValid("redis://redishost:6400"),
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_PORT": "tcp://redishost:6400",
	}
	// not applicable for a config file
	return c
}

func makeValidConfigConsulMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Consul - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Consul = ConsulConfig{
			Host: defaultConsulHost,
		}
	}
	c.envVars = map[string]string{
		"USE_CONSUL": "1",
	}
	c.fileContent = `
[Consul]
Host = "localhost"
`
	return c
}

func makeValidConfigConsulAll() testDataValidConfig {
	c := testDataValidConfig{name: "Consul - all parameters"}
	c.makeConfig =
		func(c *Config) {
			c.Consul = ConsulConfig{
				Host:     "consulhost",
				LocalTTL: NewOptDuration(3 * time.Second),
			}
		}
	c.envVars = map[string]string{
		"USE_CONSUL":  "1",
		"CONSUL_HOST": "consulhost",
		"CACHE_TTL":   "3s",
	}
	c.fileContent = `
[Consul]
Host = "consulhost"
LocalTTL = 3s
`
	return c
}

func makeValidConfigDynamoDBMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "DynamoDB - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.DynamoDB = DynamoDBConfig{
			Enabled: true,
		}
	}
	c.envVars = map[string]string{
		"USE_DYNAMODB": "1",
	}
	c.fileContent = `
[DynamoDB]
Enabled = true
`
	return c
}

func makeValidConfigDynamoDBAll() testDataValidConfig {
	c := testDataValidConfig{name: "DynamoDB - all parameters"}
	c.makeConfig = func(c *Config) {
		c.DynamoDB = DynamoDBConfig{
			Enabled:   true,
			TableName: "table",
			URL:       newOptAbsoluteURLMustBeValid("http://localhost:8000"),
			LocalTTL:  NewOptDuration(3 * time.Second),
		}
	}
	c.envVars = map[string]string{
		"USE_DYNAMODB":   "1",
		"DYNAMODB_TABLE": "table",
		"DYNAMODB_URL":   "http://localhost:8000",
		"CACHE_TTL":      "3s",
	}
	c.fileContent = `
[DynamoDB]
Enabled = true
TableName = "table"
URL = "http://localhost:8000"
LocalTTL = 3s
`
	return c
}

func makeValidConfigDatadogMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Datadog - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Datadog = DatadogConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
		}
	}
	c.envVars = map[string]string{
		"USE_DATADOG": "1",
	}
	c.fileContent = `
[Datadog]
Enabled = true
`
	return c
}

func makeValidConfigDatadogAll() testDataValidConfig {
	c := testDataValidConfig{name: "Datadog - all parameters"}
	c.makeConfig = func(c *Config) {
		c.Datadog = DatadogConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
			TraceAddr:           "trace",
			StatsAddr:           "stats",
			Tag:                 []string{"tag1:value1", "tag2:value2"},
		}
	}
	c.envVars = map[string]string{
		"USE_DATADOG":        "1",
		"DATADOG_PREFIX":     "pre-",
		"DATADOG_TRACE_ADDR": "trace",
		"DATADOG_STATS_ADDR": "stats",
		"DATADOG_TAG_tag1":   "value1",
		"DATADOG_TAG_tag2":   "value2",
	}
	c.fileContent = `
[Datadog]
Enabled = true
Prefix = "pre-"
TraceAddr = "trace"
StatsAddr = "stats"
Tag = "tag1:value1"
Tag = "tag2:value2"
`
	return c
}

func makeValidConfigStackdriverMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Stackdriver - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Stackdriver = StackdriverConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
		}
	}
	c.envVars = map[string]string{
		"USE_STACKDRIVER": "1",
	}
	c.fileContent = `
[Stackdriver]
Enabled = true
`
	return c
}

func makeValidConfigStackdriverAll() testDataValidConfig {
	c := testDataValidConfig{name: "Stackdriver - all parameters"}
	c.makeConfig = func(c *Config) {
		c.Stackdriver = StackdriverConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
			ProjectID:           "proj",
		}
	}
	c.envVars = map[string]string{
		"USE_STACKDRIVER":        "1",
		"STACKDRIVER_PREFIX":     "pre-",
		"STACKDRIVER_PROJECT_ID": "proj",
	}
	c.fileContent = `
[Stackdriver]
Enabled = true
Prefix = "pre-"
ProjectID = "proj"
`
	return c
}

func makeValidConfigPrometheusMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Prometheus - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Prometheus = PrometheusConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: ""},
			Port:                8031,
		}
	}
	c.envVars = map[string]string{
		"USE_PROMETHEUS": "1",
	}
	c.fileContent = `
[Prometheus]
Enabled = true
`
	return c
}

func makeValidConfigPrometheusAll() testDataValidConfig {
	c := testDataValidConfig{name: "Prometheus - all parameters"}
	c.makeConfig = func(c *Config) {
		c.Prometheus = PrometheusConfig{
			CommonMetricsConfig: CommonMetricsConfig{Enabled: true, Prefix: "pre-"},
			Port:                8333,
		}
	}
	c.envVars = map[string]string{
		"USE_PROMETHEUS":    "1",
		"PROMETHEUS_PREFIX": "pre-",
		"PROMETHEUS_PORT":   "8333",
	}
	c.fileContent = `
[Prometheus]
Enabled = true
Prefix = "pre-"
Port = 8333
`
	return c
}

func makeValidConfigProxy() testDataValidConfig {
	c := testDataValidConfig{name: "proxy"}
	c.makeConfig = func(c *Config) {
		c.Proxy = ProxyConfig{
			URL:         newOptAbsoluteURLMustBeValid("http://proxy"),
			User:        "user",
			Password:    "pass",
			Domain:      "domain",
			NTLMAuth:    true,
			CACertFiles: "cert",
		}
	}
	c.envVars = map[string]string{
		"PROXY_URL":           "http://proxy",
		"PROXY_AUTH_USER":     "user",
		"PROXY_AUTH_PASSWORD": "pass",
		"PROXY_AUTH_DOMAIN":   "domain",
		"PROXY_AUTH_NTLM":     "1",
		"PROXY_CA_CERTS":      "cert",
	}
	c.fileContent = `
[Proxy]
URL = "http://proxy"
User = "user"
Password = "pass"
Domain = "domain"
NTLMAuth = true
CaCertFiles = "cert"
`
	return c
}

func makeInvalidConfigTLSWithNoCertOrKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without cert/key"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1"}
	return c
}

func makeInvalidConfigTLSWithNoCert() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without cert"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1", "TLS_KEY": "key"}
	return c
}

func makeInvalidConfigTLSWithNoKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without key"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1", "TLS_CERT": "cert"}
	return c
}

func makeInvalidConfigRedisInvalidHostname() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Redis - invalid hostname"}
	c.envVarsError = "invalid Redis hostname"
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_HOST": "\\",
	}
	c.fileContent = `
[Redis]
Host = "\\"
`
	return c
}

func makeInvalidConfigRedisConflictingParams() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Redis - conflicting parameters"}
	c.envVarsError = "please specify Redis URL or host/port, but not both"
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_HOST": "redishost",
		"REDIS_URL":  "http://redishost:6400",
	}
	c.fileContent = `
[Redis]
Host = "redishost"
Url = "http://redishost:6400"
`
	return c
}

func makeInvalidConfigMultipleDatabases() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "multiple databases are enabled"}
	c.envVarsError = "multiple databases are enabled (Redis, Consul, DynamoDB); only one is allowed"
	c.envVars = map[string]string{
		"USE_REDIS":    "1",
		"USE_CONSUL":   "1",
		"USE_DYNAMODB": "1",
	}
	c.fileContent = `
[Redis]
Host = "localhost"

[Consul]
Host = "consulhost"

[DynamoDB]
Enabled = true
`
	return c
}
