package config

import (
	"crypto/tls"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
)

type testDataValidConfig struct {
	name        string
	makeConfig  func(c *Config)
	envVars     map[string]string
	fileContent string
	warnings    []string
}

var configDefaults = Config{
	Main: MainConfig{
		BaseURI:           defaultBaseURI,
		ClientSideBaseURI: defaultClientSideBaseURI,
		StreamURI:         defaultStreamURI,
	},
	Events: EventsConfig{
		EventsURI: defaultEventsURI,
	},
}

func (tdc testDataValidConfig) assertResult(t *testing.T, actualConfig Config, mockLog *ldlogtest.MockLog) {
	expectedConfig := configDefaults
	tdc.makeConfig(&expectedConfig)
	assert.Equal(t, expectedConfig, actualConfig)
	for _, message := range tdc.warnings {
		mockLog.AssertMessageMatch(t, true, ldlog.Warn, regexp.QuoteMeta(message))
	}
}

func mustOptIntGreaterThanZero(n int) ct.OptIntGreaterThanZero {
	o, err := ct.NewOptIntGreaterThanZero(n)
	if err != nil {
		panic(err)
	}
	return o
}

func newOptURLAbsoluteMustBeValid(urlString string) ct.OptURLAbsolute {
	o, err := ct.NewOptURLAbsoluteFromString(urlString)
	if err != nil {
		panic(err)
	}
	return o
}

func makeValidConfigs() []testDataValidConfig {
	return []testDataValidConfig{
		makeValidConfigAllBaseProperties(),
		makeValidConfigCustomBaseURIOnly(),
		makeValidConfigExplicitDefaultBaseURI(),
		makeValidConfigExplicitOldDefaultBaseURI(),
		makeValidConfigAutoConfig(),
		makeValidConfigAutoConfigWithDatabase(),
		makeValidConfigFileData(),
		makeValidConfigRedisMinimal(),
		makeValidConfigRedisAll(),
		makeValidConfigRedisURL(),
		makeValidConfigRedisPortOnly(),
		makeValidConfigRedisDockerPort(),
		makeValidConfigRedisOneEnvNoPrefix(),
		makeValidConfigConsulMinimal(),
		makeValidConfigConsulAll(),
		makeValidConfigConsulOneEnvNoPrefix(),
		makeValidConfigConsulToken(),
		makeValidConfigConsulTokenFile(),
		makeValidConfigDynamoDBMinimal(),
		makeValidConfigDynamoDBAll(),
		makeValidConfigDynamoDBMultiEnvsWithTable(),
		makeValidConfigDynamoDBOneEnvNoPrefixOrTable(),
		makeValidConfigDatadogMinimal(),
		makeValidConfigDatadogAll(),
		makeValidConfigStackdriverMinimal(),
		makeValidConfigStackdriverAll(),
		makeValidConfigPrometheusMinimal(),
		makeValidConfigPrometheusAll(),
		makeValidConfigProxy(),
	}
}

func makeValidConfigAllBaseProperties() testDataValidConfig {
	c := testDataValidConfig{name: "all base properties"}
	c.makeConfig = func(c *Config) {
		c.Main = MainConfig{
			Port:                        mustOptIntGreaterThanZero(8333),
			BaseURI:                     newOptURLAbsoluteMustBeValid("http://base"),
			ClientSideBaseURI:           newOptURLAbsoluteMustBeValid("http://clientbase"),
			StreamURI:                   newOptURLAbsoluteMustBeValid("http://stream"),
			ExitOnError:                 true,
			ExitAlways:                  true,
			IgnoreConnectionErrors:      true,
			HeartbeatInterval:           ct.NewOptDuration(90 * time.Second),
			MaxClientConnectionTime:     ct.NewOptDuration(30 * time.Minute),
			DisconnectedStatusTime:      ct.NewOptDuration(3 * time.Minute),
			DisableInternalUsageMetrics: true,
			TLSEnabled:                  true,
			TLSCert:                     "cert",
			TLSKey:                      "key",
			TLSMinVersion:               NewOptTLSVersion(tls.VersionTLS12),
			LogLevel:                    NewOptLogLevel(ldlog.Warn),
			BigSegmentsStaleAsDegraded:  true,
			BigSegmentsStaleThreshold:   ct.NewOptDuration(10 * time.Minute),
		}
		c.Events = EventsConfig{
			SendEvents:    true,
			EventsURI:     newOptURLAbsoluteMustBeValid("http://events"),
			FlushInterval: ct.NewOptDuration(120 * time.Second),
			Capacity:      mustOptIntGreaterThanZero(500),
			InlineUsers:   true,
		}
		c.Environment = map[string]*EnvConfig{
			"earth": {
				SDKKey:    "earth-sdk",
				MobileKey: "earth-mob",
				EnvID:     "earth-env",
				Prefix:    "earth-",
				TableName: "earth-table",
				LogLevel:  NewOptLogLevel(ldlog.Debug),
			},
			"krypton": {
				SDKKey:        "krypton-sdk",
				MobileKey:     "krypton-mob",
				EnvID:         "krypton-env",
				SecureMode:    true,
				Prefix:        "krypton-",
				TableName:     "krypton-table",
				AllowedOrigin: ct.NewOptStringList([]string{"https://oa", "https://rann"}),
				AllowedHeader: ct.NewOptStringList([]string{"Timestamp-Valid", "Random-Id-Valid"}),
				TTL:           ct.NewOptDuration(5 * time.Minute),
			},
		}
	}
	c.envVars = map[string]string{
		"PORT":                           "8333",
		"BASE_URI":                       "http://base",
		"CLIENT_SIDE_BASE_URI":           "http://clientbase",
		"STREAM_URI":                     "http://stream",
		"EXIT_ON_ERROR":                  "1",
		"EXIT_ALWAYS":                    "1",
		"IGNORE_CONNECTION_ERRORS":       "1",
		"HEARTBEAT_INTERVAL":             "90s",
		"MAX_CLIENT_CONNECTION_TIME":     "30m",
		"DISCONNECTED_STATUS_TIME":       "3m",
		"DISABLE_INTERNAL_USAGE_METRICS": "1",
		"TLS_ENABLED":                    "1",
		"TLS_CERT":                       "cert",
		"TLS_KEY":                        "key",
		"TLS_MIN_VERSION":                "1.2",
		"LOG_LEVEL":                      "warn",
		"BIG_SEGMENTS_STALE_AS_DEGRADED": "true",
		"BIG_SEGMENTS_STALE_THRESHOLD":   "10m",
		"USE_EVENTS":                     "1",
		"EVENTS_HOST":                    "http://events",
		"EVENTS_FLUSH_INTERVAL":          "120s",
		"EVENTS_CAPACITY":                "500",
		"EVENTS_INLINE_USERS":            "1",
		"LD_ENV_earth":                   "earth-sdk",
		"LD_MOBILE_KEY_earth":            "earth-mob",
		"LD_CLIENT_SIDE_ID_earth":        "earth-env",
		"LD_PREFIX_earth":                "earth-",
		"LD_TABLE_NAME_earth":            "earth-table",
		"LD_LOG_LEVEL_earth":             "debug",
		"LD_ENV_krypton":                 "krypton-sdk",
		"LD_MOBILE_KEY_krypton":          "krypton-mob",
		"LD_CLIENT_SIDE_ID_krypton":      "krypton-env",
		"LD_SECURE_MODE_krypton":         "1",
		"LD_PREFIX_krypton":              "krypton-",
		"LD_TABLE_NAME_krypton":          "krypton-table",
		"LD_ALLOWED_ORIGIN_krypton":      "https://oa,https://rann",
		"LD_ALLOWED_HEADER_krypton":      "Timestamp-Valid,Random-Id-Valid",
		"LD_TTL_krypton":                 "5m",
	}
	c.fileContent = `
[Main]
Port = 8333
BaseUri = "http://base"
ClientSideBaseUri = "http://clientbase"
StreamUri = "http://stream"
ExitOnError = 1
ExitAlways = 1
IgnoreConnectionErrors = 1
HeartbeatInterval = 90s
MaxClientConnectionTime = 30m
DisconnectedStatusTime = 3m
DisableInternalUsageMetrics = 1
TLSEnabled = 1
TLSCert = "cert"
TLSKey = "key"
TLSMinVersion = "1.2"
LogLevel = "warn"
BigSegmentsStaleAsDegraded = 1
BigSegmentsStaleThreshold = 10m

[Events]
SendEvents = 1
EventsUri = "http://events"
FlushInterval = 120s
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
SecureMode = true
Prefix = "krypton-"
TableName = "krypton-table"
AllowedOrigin = "https://oa"
AllowedOrigin = "https://rann"
AllowedHeader = "Timestamp-Valid"
AllowedHeader = "Random-Id-Valid"
TTL = 5m
`
	return c
}

func makeValidConfigAutoConfig() testDataValidConfig {
	c := testDataValidConfig{name: "auto-config properties"}
	c.makeConfig = func(c *Config) {
		c.AutoConfig = AutoConfigConfig{
			Key:              AutoConfigKey("autokey"),
			EnvAllowedOrigin: ct.NewOptStringList([]string{"http://first", "http://second"}),
			EnvAllowedHeader: ct.NewOptStringList([]string{"First", "Second"}),
		}
	}
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY":    "autokey",
		"ENV_ALLOWED_ORIGIN": "http://first,http://second",
		"ENV_ALLOWED_HEADER": "First,Second",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey
EnvAllowedOrigin = http://first
EnvAllowedOrigin = http://second
EnvAllowedHeader = First
EnvAllowedHeader = Second
`
	return c
}

func makeValidConfigCustomBaseURIOnly() testDataValidConfig {
	c := testDataValidConfig{name: "custom base URI"}
	c.makeConfig = func(c *Config) {
		c.Main.BaseURI = newOptURLAbsoluteMustBeValid("http://custom-base")
		c.Main.ClientSideBaseURI = c.Main.BaseURI
	}
	c.envVars = map[string]string{
		"BASE_URI": "http://custom-base",
	}
	c.fileContent = `
[Main]
BaseURI = http://custom-base
`
	return c
}

func makeValidConfigExplicitDefaultBaseURI() testDataValidConfig {
	c := testDataValidConfig{name: "base URI explicitly set to default"}
	c.makeConfig = func(c *Config) {}
	c.envVars = map[string]string{
		"BASE_URI": "https://sdk.launchdarkly.com",
	}
	c.fileContent = `
[Main]
BaseURI = https://sdk.launchdarkly.com
`
	return c
}

func makeValidConfigExplicitOldDefaultBaseURI() testDataValidConfig {
	c := testDataValidConfig{name: "base URI explicitly set to old default"}
	c.makeConfig = func(c *Config) {}
	c.envVars = map[string]string{
		"BASE_URI": "https://app.launchdarkly.com",
	}
	c.fileContent = `
[Main]
BaseURI = https://app.launchdarkly.com
`
	return c
}

func makeValidConfigAutoConfigWithDatabase() testDataValidConfig {
	c := testDataValidConfig{name: "auto-config properties with database"}
	c.makeConfig = func(c *Config) {
		c.AutoConfig = AutoConfigConfig{
			Key:                   AutoConfigKey("autokey"),
			EnvDatastorePrefix:    "prefix-$CID",
			EnvDatastoreTableName: "table-$CID",
			EnvAllowedOrigin:      ct.NewOptStringList([]string{"http://first", "http://second"}),
			EnvAllowedHeader:      ct.NewOptStringList([]string{"First", "Second"}),
		}
		c.DynamoDB.Enabled = true
	}
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY":          "autokey",
		"ENV_DATASTORE_PREFIX":     "prefix-$CID",
		"ENV_DATASTORE_TABLE_NAME": "table-$CID",
		"ENV_ALLOWED_ORIGIN":       "http://first,http://second",
		"ENV_ALLOWED_HEADER":       "First,Second",
		"USE_DYNAMODB":             "1",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey
EnvDatastorePrefix = prefix-$CID
EnvDatastoreTableName = table-$CID
EnvAllowedOrigin = http://first
EnvAllowedOrigin = http://second
EnvAllowedHeader = First
EnvAllowedHeader = Second

[DynamoDB]
Enabled = true
`
	return c
}

func makeValidConfigFileData() testDataValidConfig {
	c := testDataValidConfig{name: "file data properties"}
	c.makeConfig = func(c *Config) {
		c.OfflineMode.FileDataSource = "my-file-path"
	}
	c.envVars = map[string]string{
		"FILE_DATA_SOURCE": "my-file-path",
	}
	c.fileContent = `
[OfflineMode]
FileDataSource = my-file-path
`
	return c
}

func makeValidConfigRedisMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptURLAbsoluteMustBeValid("redis://localhost:6379"),
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
			URL:      newOptURLAbsoluteMustBeValid("redis://redishost:6400"),
			LocalTTL: ct.NewOptDuration(3 * time.Second),
			TLS:      true,
			Password: "pass",
			Username: "user",
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS":      "1",
		"REDIS_HOST":     "redishost",
		"REDIS_PORT":     "6400",
		"REDIS_TLS":      "1",
		"REDIS_PASSWORD": "pass",
		"REDIS_USERNAME": "user",
		"CACHE_TTL":      "3s",
	}
	c.fileContent = `
[Redis]
Host = "redishost"
Port = 6400
TLS = 1
Password = "pass"
Username = "user"
LocalTTL = 3s
`
	return c
}

func makeValidConfigRedisURL() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - URL instead of host/port"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptURLAbsoluteMustBeValid("rediss://redishost:6400"),
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
			URL: newOptURLAbsoluteMustBeValid("redis://localhost:9999"),
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
			URL: newOptURLAbsoluteMustBeValid("redis://redishost:6400"),
		}
	}
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_PORT": "tcp://redishost:6400",
	}
	// not applicable for a config file
	return c
}

func makeValidConfigRedisOneEnvNoPrefix() testDataValidConfig {
	c := testDataValidConfig{name: "Redis - single env, no prefix (warning)"}
	c.makeConfig = func(c *Config) {
		c.Redis = RedisConfig{
			URL: newOptURLAbsoluteMustBeValid("redis://localhost:6379"),
		}
		c.Environment = map[string]*EnvConfig{
			"env1": {SDKKey: SDKKey("key1")},
		}
	}
	c.envVars = map[string]string{
		"LD_ENV_env1": "key1",
		"USE_REDIS":   "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1

[Redis]
Host = localhost
`
	c.warnings = []string{warnEnvWithoutDBDisambiguation("env1", false)}
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
				LocalTTL: ct.NewOptDuration(3 * time.Second),
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

func makeValidConfigConsulOneEnvNoPrefix() testDataValidConfig {
	c := testDataValidConfig{name: "Consul - single env, no prefix (warning)"}
	c.makeConfig = func(c *Config) {
		c.Consul = ConsulConfig{
			Host: defaultConsulHost,
		}
		c.Environment = map[string]*EnvConfig{
			"env1": {SDKKey: SDKKey("key1")},
		}
	}
	c.envVars = map[string]string{
		"LD_ENV_env1": "key1",
		"USE_CONSUL":  "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1

[Consul]
Host = localhost
`
	c.warnings = []string{warnEnvWithoutDBDisambiguation("env1", false)}
	return c
}

func makeValidConfigConsulToken() testDataValidConfig {
	c := testDataValidConfig{name: "Consul - token"}
	c.makeConfig = func(c *Config) {
		c.Consul = ConsulConfig{
			Host:  defaultConsulHost,
			Token: "abc",
		}
	}
	c.envVars = map[string]string{
		"USE_CONSUL":   "1",
		"CONSUL_TOKEN": "abc",
	}
	c.fileContent = `
[Consul]
Host = localhost
Token = abc
`
	return c
}

func makeValidConfigConsulTokenFile() testDataValidConfig {
	aFileThatExists := "./config.go" // doesn't contain a token, but is a file that exists
	c := testDataValidConfig{name: "Consul - token file"}
	c.makeConfig = func(c *Config) {
		c.Consul = ConsulConfig{
			Host:      defaultConsulHost,
			TokenFile: aFileThatExists, // doesn't contain a token, but is a file that exists
		}
	}
	c.envVars = map[string]string{
		"USE_CONSUL":        "1",
		"CONSUL_TOKEN_FILE": aFileThatExists,
	}
	c.fileContent = `
[Consul]
Host = localhost
TokenFile = ` + aFileThatExists + `
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
			URL:       newOptURLAbsoluteMustBeValid("http://localhost:8000"),
			LocalTTL:  ct.NewOptDuration(3 * time.Second),
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

func makeValidConfigDynamoDBMultiEnvsWithTable() testDataValidConfig {
	c := testDataValidConfig{name: "DynamoDB - multiple envs, table name defined instead of prefix"}
	c.makeConfig = func(c *Config) {
		c.DynamoDB = DynamoDBConfig{
			Enabled: true,
		}
		c.Environment = map[string]*EnvConfig{
			"env1": {SDKKey: SDKKey("key1"), TableName: "table1"},
			"env2": {SDKKey: SDKKey("key2"), TableName: "table2"},
		}
	}
	c.envVars = map[string]string{
		"LD_ENV_env1":        "key1",
		"LD_TABLE_NAME_env1": "table1",
		"LD_ENV_env2":        "key2",
		"LD_TABLE_NAME_env2": "table2",
		"USE_DYNAMODB":       "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1
TableName = table1

[Environment "env2"]
SdkKey = key2
TableName = table2

[DynamoDB]
Enabled = true
`
	return c
}

func makeValidConfigDynamoDBOneEnvNoPrefixOrTable() testDataValidConfig {
	c := testDataValidConfig{name: "DynamoDB - single env, no prefix or table name (warning)"}
	c.makeConfig = func(c *Config) {
		c.DynamoDB = DynamoDBConfig{
			Enabled: true,
		}
		c.Environment = map[string]*EnvConfig{
			"env1": {SDKKey: SDKKey("key1")},
		}
	}
	c.envVars = map[string]string{
		"LD_ENV_env1":  "key1",
		"USE_DYNAMODB": "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1

[DynamoDB]
Enabled = true
`
	c.warnings = []string{warnEnvWithoutDBDisambiguation("env1", true)}
	return c
}

func makeValidConfigDatadogMinimal() testDataValidConfig {
	c := testDataValidConfig{name: "Datadog - minimal parameters"}
	c.makeConfig = func(c *Config) {
		c.Datadog = DatadogConfig{
			Enabled: true,
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
			Enabled:   true,
			Prefix:    "pre-",
			TraceAddr: "trace",
			StatsAddr: "stats",
			Tag:       []string{"tag1:value1", "tag2:value2"},
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
			Enabled: true,
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
			Enabled:   true,
			Prefix:    "pre-",
			ProjectID: "proj",
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
			Enabled: true,
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
			Enabled: true,
			Prefix:  "pre-",
			Port:    mustOptIntGreaterThanZero(8333),
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
			URL:         newOptURLAbsoluteMustBeValid("http://proxy"),
			User:        "user",
			Password:    "pass",
			Domain:      "domain",
			NTLMAuth:    true,
			CACertFiles: ct.NewOptStringList([]string{"cert"}),
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
