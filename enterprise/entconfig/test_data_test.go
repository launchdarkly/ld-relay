package entconfig

import (
	"time"

	config "github.com/launchdarkly/ld-relay-config"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type testDataValidConfig struct {
	name        string
	makeConfig  func(c *EnterpriseConfig)
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
	}
}

func makeInvalidConfigs() []testDataInvalidConfig {
	return []testDataInvalidConfig{
		makeInvalidConfigParsingErrorInBaseConfig(),
		makeInvalidConfigValidationErrorInBaseConfig(),
		makeInvalidConfigMultipleValidationErrorsInBaseConfig(),
		makeInvalidConfigAutoConfKeyWithEnvironments(),
		makeInvalidConfigAutoConfAllowedOriginWithNoKey(),
		makeInvalidConfigAutoConfPrefixWithNoKey(),
		makeInvalidConfigAutoConfTableNameWithNoKey(),
	}
}

func makeValidConfigAllBaseProperties() testDataValidConfig {
	c := testDataValidConfig{name: "all base properties"}
	c.makeConfig = func(c *EnterpriseConfig) {
		c.Config.Main = config.MainConfig{
			Port:                    mustOptIntGreaterThanZero(8333),
			BaseURI:                 newOptURLAbsoluteMustBeValid("http://base"),
			StreamURI:               newOptURLAbsoluteMustBeValid("http://stream"),
			ExitOnError:             true,
			ExitAlways:              true,
			IgnoreConnectionErrors:  true,
			HeartbeatInterval:       ct.NewOptDuration(90 * time.Second),
			MaxClientConnectionTime: ct.NewOptDuration(30 * time.Minute),
			TLSEnabled:              true,
			TLSCert:                 "cert",
			TLSKey:                  "key",
			LogLevel:                config.NewOptLogLevel(ldlog.Warn),
		}
		c.AutoConfig = AutoConfigConfig{
			Key:                   AutoConfigKey("autokey"),
			EnvDatastorePrefix:    "prefix-$CID",
			EnvDatastoreTableName: "table-$CID",
			EnvAllowedOrigin:      ct.NewOptStringList([]string{"http://first", "http://second"}),
		}
	}
	c.envVars = map[string]string{
		"PORT":                       "8333",
		"BASE_URI":                   "http://base",
		"STREAM_URI":                 "http://stream",
		"EXIT_ON_ERROR":              "1",
		"EXIT_ALWAYS":                "1",
		"IGNORE_CONNECTION_ERRORS":   "1",
		"HEARTBEAT_INTERVAL":         "90s",
		"MAX_CLIENT_CONNECTION_TIME": "30m",
		"TLS_ENABLED":                "1",
		"TLS_CERT":                   "cert",
		"TLS_KEY":                    "key",
		"LOG_LEVEL":                  "warn",
		"AUTO_CONFIG_KEY":            "autokey",
		"ENV_DATASTORE_PREFIX":       "prefix-$CID",
		"ENV_DATASTORE_TABLE_NAME":   "table-$CID",
		"ENV_ALLOWED_ORIGIN":         "http://first,http://second",
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
MaxClientConnectionTime = 30m
TLSEnabled = 1
TLSCert = "cert"
TLSKey = "key"
LogLevel = "warn"

[AutoConfig]
Key = autokey
EnvDatastorePrefix = prefix-$CID
EnvDatastoreTableName = table-$CID
EnvAllowedOrigin = http://first
EnvAllowedOrigin = http://second
`
	return c
}

func makeInvalidConfigParsingErrorInBaseConfig() testDataInvalidConfig {
	// We already test all the error scenarios in the unit tests for the Core config, but here we're
	// verifying that Enterprise actually checks for errors returned by the Core config.
	c := testDataInvalidConfig{name: "parsing error in base config"}
	c.envVarsError = "not a valid boolean value"
	c.envVars = map[string]string{
		"EXIT_ON_ERROR": "invalid",
	}
	c.fileContent = `
[Main]
ExitOnError = invalid
`
	c.fileError = "failed to parse bool"
	return c
}

func makeInvalidConfigValidationErrorInBaseConfig() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "validation error in base config"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{
		"TLS_ENABLED": "1",
	}
	c.fileContent = `
[Main]
TLSEnabled = true
`
	return c
}

func makeInvalidConfigMultipleValidationErrorsInBaseConfig() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "validation error in base config"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled, multiple databases are enabled"
	c.envVars = map[string]string{
		"TLS_ENABLED":  "1",
		"USE_REDIS":    "1",
		"USE_DYNAMODB": "1",
	}
	c.fileContent = `
[Main]
TLSEnabled = true

[Redis]
Host = localhost

[DynamoDB]
Enabled = true
`
	return c
}

func makeInvalidConfigAutoConfKeyWithEnvironments() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "auto-conf key with environments"}
	c.envVarsError = errAutoConfWithEnvironments.Error()
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY": "autokey",
		"LD_ENV_envname":  "sdk-key",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey

[Environment "envname"]
SDKKey = sdk-key
`
	return c
}

func makeInvalidConfigAutoConfAllowedOriginWithNoKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "auto-conf allowed origin with no key"}
	c.envVarsError = errAutoConfPropertiesWithNoKey.Error()
	c.envVars = map[string]string{
		"ENV_ALLOWED_ORIGIN": "http://origin",
	}
	c.fileContent = `
[AutoConfig]
EnvAllowedOrigin = http://origin
`
	return c
}

func makeInvalidConfigAutoConfPrefixWithNoKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "auto-conf prefix with no key"}
	c.envVarsError = errAutoConfPropertiesWithNoKey.Error()
	c.envVars = map[string]string{
		"ENV_DATASTORE_PREFIX": "prefix",
	}
	c.fileContent = `
[AutoConfig]
EnvDatastorePrefix = prefix
`
	return c
}

func makeInvalidConfigAutoConfTableNameWithNoKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "auto-conf table name with no key"}
	c.envVarsError = errAutoConfPropertiesWithNoKey.Error()
	c.envVars = map[string]string{
		"ENV_DATASTORE_TABLE_NAME": "table",
	}
	c.fileContent = `
[AutoConfig]
EnvDatastoreTableName = table
`
	return c
}
