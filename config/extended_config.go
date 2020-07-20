package config

import ct "github.com/launchdarkly/go-configtypes"

// This file contains extended configuration for Relay Proxy Enterprise. It will be moved to a
// separate project later in development.

const (
	// This string can appear within MainExtendedConfig.EnvDataStorePrefix or
	// MainExtendedConfig.EnvDataStoreTableName to indicate that the environment ID should be
	// substituted at that point.
	//
	// For instance, if EnvDataStorePrefix is "LD-$CID", the value of that setting for an environment
	// whose ID is "12345" would be "LD-12345".
	AutoConfigEnvironmentIDPlaceholder = "$CID"
)

type ExtendedConfig struct {
	Main        MainExtendedConfig
	Events      EventsConfig
	Redis       RedisConfig
	Consul      ConsulConfig
	DynamoDB    DynamoDBConfig
	Environment map[string]*EnvConfig
	Proxy       ProxyConfig

	// Optional configuration for metrics integrations. Note that unlike the other fields in Config,
	// MetricsConfig is not the name of a configuration file section; the actual sections are the
	// structs within this struct (Datadog, etc.).
	MetricsConfig
}

// MainConfig contains global configuration options for Relay Proxy Enterprise.
//
// This corresponds to the [Main] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type MainExtendedConfig struct {
	MainConfig
	AutoConfigKey         string           `conf:"AUTO_CONFIG_KEY"`
	EnvDatastorePrefix    string           `conf:"ENV_DATASTORE_PREFIX"`
	EnvDatastoreTableName string           `conf:"ENV_DATASTORE_TABLE_NAME"`
	EnvAllowedOrigin      ct.OptStringList `conf:"ENV_ALLOWED_ORIGIN"`
}
