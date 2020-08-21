package entconfig

import (
	ct "github.com/launchdarkly/go-configtypes"
	config "github.com/launchdarkly/ld-relay-config"
)

// This file contains extended configuration for Relay Proxy Enterprise. It will be moved to a
// separate project later in development.

const (
	// AutoConfigEnvironmentIDPlaceholder is a string that can appear within
	// AutoConfigConfig.EnvDataStorePrefix or AutoConfigConfig.EnvDataStoreTableName to indicate that
	// the environment ID should be substituted at that point.
	//
	// For instance, if EnvDataStorePrefix is "LD-$CID", the value of that setting for an environment
	// whose ID is "12345" would be "LD-12345".
	AutoConfigEnvironmentIDPlaceholder = "$CID"
)

// EnterpriseConfig describes the configuration for a RelayEnterprise instance.
//
// This is mostly the same as regular Relay, with some added options.
type EnterpriseConfig struct {
	config.Config
	AutoConfig AutoConfigConfig
}

// AutoConfigConfig contains configuration parameters for the auto-configuration feature.
type AutoConfigConfig struct {
	Key                   AutoConfigKey    `conf:"AUTO_CONFIG_KEY"`
	EnvDatastorePrefix    string           `conf:"ENV_DATASTORE_PREFIX"`
	EnvDatastoreTableName string           `conf:"ENV_DATASTORE_TABLE_NAME"`
	EnvAllowedOrigin      ct.OptStringList `conf:"ENV_ALLOWED_ORIGIN"`
}
