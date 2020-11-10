package config

type testDataInvalidConfig struct {
	name         string
	envVarsError string
	fileError    string
	envVars      map[string]string
	fileContent  string
}

func makeInvalidConfigs() []testDataInvalidConfig {
	return []testDataInvalidConfig{
		makeInvalidConfigMissingSDKKey(),
		makeInvalidConfigTLSWithNoCertOrKey(),
		makeInvalidConfigTLSWithNoCert(),
		makeInvalidConfigTLSWithNoKey(),
		makeInvalidConfigTLSVersion(),
		makeInvalidConfigAutoConfKeyWithEnvironments(),
		makeInvalidConfigAutoConfAllowedOriginWithNoKey(),
		makeInvalidConfigAutoConfPrefixWithNoKey(),
		makeInvalidConfigAutoConfTableNameWithNoKey(),
		makeInvalidConfigFileDataWithAutoConfKey(),
		makeInvalidConfigFileDataWithEnvironments(),
		makeInvalidConfigOfflineModeAllowedOriginWithNoFile(),
		makeInvalidConfigOfflineModePrefixWithNoFile(),
		makeInvalidConfigOfflineModeTableNameWithNoFile(),
		makeInvalidConfigRedisInvalidHostname(),
		makeInvalidConfigRedisInvalidDockerPort(),
		makeInvalidConfigRedisConflictingParams(),
		makeInvalidConfigRedisNoPrefix(),
		makeInvalidConfigRedisAutoConfNoPrefix(),
		makeInvalidConfigConsulNoPrefix(),
		makeInvalidConfigConsulAutoConfNoPrefix(),
		makeInvalidConfigConsulTokenAndTokenFile(),
		makeInvalidConfigDynamoDBNoPrefixOrTableName(),
		makeInvalidConfigDynamoDBAutoConfNoPrefixOrTableName(),
		makeInvalidConfigMultipleDatabases(),
	}
}

func makeInvalidConfigMissingSDKKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "environment without SDK key"}
	c.fileContent = `
[Environment "envname"]
MobileKey = mob-xxx
`
	c.fileError = `SDK key is required for environment "envname"`
	return c
}

func makeInvalidConfigTLSWithNoCertOrKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without cert/key"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1"}
	c.fileContent = `
[Main]
TLSEnabled = true
`
	return c
}

func makeInvalidConfigTLSWithNoCert() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without cert"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1", "TLS_KEY": "key"}
	c.fileContent = `
[Main]
TLSEnabled = true
TLSKey = keyfile
`
	return c
}

func makeInvalidConfigTLSWithNoKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "TLS without key"}
	c.envVarsError = "TLS cert and key are required if TLS is enabled"
	c.envVars = map[string]string{"TLS_ENABLED": "1", "TLS_CERT": "cert"}
	c.fileContent = `
[Main]
TLSEnabled = true
TLSCert = certfile
`
	return c
}

func makeInvalidConfigTLSVersion() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "bad TLS version"}
	c.envVarsError = "not a valid TLS version"
	c.envVars = map[string]string{"TLS_ENABLED": "1", "TLS_MIN_VERSION": "x"}
	c.fileContent = `
[Main]
TLSEnabled = true
TLSMinVersion = x
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

func makeInvalidConfigFileDataWithAutoConfKey() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "file data source with auto-config key"}
	c.envVarsError = errFileDataWithAutoConf.Error()
	c.envVars = map[string]string{
		"FILE_DATA_SOURCE": "my-file-path",
		"AUTO_CONFIG_KEY":  "autokey",
	}
	c.fileContent = `
[OfflineMode]
FileDataSource = my-file-path

[AutoConfig]
Key = autokey
`
	return c
}

func makeInvalidConfigFileDataWithEnvironments() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "file data source with environments"}
	c.envVarsError = errOfflineModeWithEnvironments.Error()
	c.envVars = map[string]string{
		"FILE_DATA_SOURCE": "my-file-path",
		"LD_ENV_envname":   "sdk-key",
	}
	c.fileContent = `
[OfflineMode]
FileDataSource = my-file-path

[Environment "envname"]
SDKKey = sdk-key
`
	return c
}

func makeInvalidConfigOfflineModeAllowedOriginWithNoFile() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "offline mode allowed origin with no file"}
	c.fileError = errOfflineModePropertiesWithNoFile.Error()
	c.fileContent = `
[OfflineMode]
EnvAllowedOrigin = http://origin
`
	return c
}

func makeInvalidConfigOfflineModePrefixWithNoFile() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "offline mode prefix with no file"}
	c.fileError = errOfflineModePropertiesWithNoFile.Error()
	c.fileContent = `
[OfflineMode]
EnvDatastorePrefix = prefix
`
	return c
}

func makeInvalidConfigOfflineModeTableNameWithNoFile() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "offline mode table name with no file"}
	c.fileError = errOfflineModePropertiesWithNoFile.Error()
	c.fileContent = `
[OfflineMode]
EnvDatastoreTableName = table
`
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

func makeInvalidConfigRedisInvalidDockerPort() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Redis - Docker port syntax with invalid port"}
	c.envVarsError = "REDIS_PORT: not a valid integer"
	c.envVars = map[string]string{
		"USE_REDIS":  "1",
		"REDIS_PORT": "tcp://redishost:xxx",
	}
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

func makeInvalidConfigRedisNoPrefix() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Redis - multiple environments, prefix not defined"}
	c.envVarsError = errEnvWithoutDBDisambiguation("env2", false).Error()
	c.envVars = map[string]string{
		"LD_ENV_env1":    "key1",
		"LD_PREFIX_env1": "prefix1",
		"LD_ENV_env2":    "key2",
		"USE_REDIS":      "1",
		"REDIS_URL":      "redis://localhost:6379",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1
Prefix = prefix1

[Environment "env2"]
SdkKey = key2

[Redis]
URL = redis://localhost:6379
`
	return c
}

func makeInvalidConfigRedisAutoConfNoPrefix() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Redis - auto-configuration, prefix not defined"}
	c.envVarsError = errAutoConfWithoutDBDisambig.Error()
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY": "autokey",
		"USE_REDIS":       "1",
		"REDIS_URL":       "redis://localhost:6379",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey

[Redis]
URL = redis://localhost:6379
`
	return c
}

func makeInvalidConfigConsulNoPrefix() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Consul - multiple environments, prefix not defined"}
	c.envVarsError = errEnvWithoutDBDisambiguation("env2", false).Error()
	c.envVars = map[string]string{
		"LD_ENV_env1":    "key1",
		"LD_PREFIX_env1": "prefix1",
		"LD_ENV_env2":    "key2",
		"USE_CONSUL":     "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1
Prefix = prefix1

[Environment "env2"]
SdkKey = key2

[Consul]
Host = localhost
`
	return c
}

func makeInvalidConfigConsulAutoConfNoPrefix() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Consul - auto-configuration, prefix not defined"}
	c.envVarsError = errAutoConfWithoutDBDisambig.Error()
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY": "autokey",
		"USE_CONSUL":      "1",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey

[Consul]
Host = localhost
`
	return c
}

func makeInvalidConfigConsulTokenAndTokenFile() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "Consul - token and token file both specified"}
	c.envVarsError = errConsulTokenAndTokenFile.Error()
	c.envVars = map[string]string{
		"USE_CONSUL":        "1",
		"CONSUL_TOKEN":      "abc",
		"CONSUL_TOKEN_FILE": "def",
	}
	c.fileContent = `
[Consul]
Host = localhost
Token = abc
TokenFile = def
`
	return c
}

func makeInvalidConfigDynamoDBNoPrefixOrTableName() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "DynamoDB - multiple environments, prefix and table name not defined"}
	c.envVarsError = errEnvWithoutDBDisambiguation("env2", true).Error()
	c.envVars = map[string]string{
		"LD_ENV_env1":    "key1",
		"LD_PREFIX_env1": "prefix1",
		"LD_ENV_env2":    "key2",
		"USE_DYNAMODB":   "1",
	}
	c.fileContent = `
[Environment "env1"]
SdkKey = key1
Prefix = prefix1

[Environment "env2"]
SdkKey = key2

[DynamoDB]
Enabled = true
`
	return c
}

func makeInvalidConfigDynamoDBAutoConfNoPrefixOrTableName() testDataInvalidConfig {
	c := testDataInvalidConfig{name: "DynamoDB - auto-configuration, prefix and table name not defined"}
	c.envVarsError = errAutoConfWithoutDBDisambig.Error()
	c.envVars = map[string]string{
		"AUTO_CONFIG_KEY": "autokey",
		"USE_DYNAMODB":    "1",
	}
	c.fileContent = `
[AutoConfig]
Key = autokey

[DynamoDB]
Enabled = true
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
