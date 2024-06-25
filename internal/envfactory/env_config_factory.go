package envfactory

import (
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"

	ct "github.com/launchdarkly/go-configtypes"
)

// EnvConfigFactory is an abstraction of the logic for generating environment configurations that
// are partly parameterized, instead of each environment being manually configured. This is used
// in both auto-configuration mode and offline mode.
type EnvConfigFactory struct {
	// DataStorePrefix is the configured data store prefix, which may contain a per-environment placeholder.
	DataStorePrefix string
	// DataStorePrefix is the configured data store table name, which may contain a per-environment placeholder.
	TableName string
	//
	AllowedOrigin ct.OptStringList
	AllowedHeader ct.OptStringList
}

// NewEnvConfigFactoryForAutoConfig creates an EnvConfigFactory based on the auto-configuration mode settings.
func NewEnvConfigFactoryForAutoConfig(c config.AutoConfigConfig) EnvConfigFactory {
	return EnvConfigFactory{
		DataStorePrefix: c.EnvDatastorePrefix,
		TableName:       c.EnvDatastoreTableName,
		AllowedOrigin:   c.EnvAllowedOrigin,
		AllowedHeader:   c.EnvAllowedHeader,
	}
}

// NewEnvConfigFactoryForOfflineMode creates an EnvConfigFactory based on the offline mode settings.
func NewEnvConfigFactoryForOfflineMode(c config.OfflineModeConfig) EnvConfigFactory {
	return EnvConfigFactory{
		DataStorePrefix: c.EnvDatastorePrefix,
		TableName:       c.EnvDatastoreTableName,
		AllowedOrigin:   c.EnvAllowedOrigin,
		AllowedHeader:   c.EnvAllowedHeader,
	}
}

// MakeEnvironmentConfig creates an EnvConfig based on both the individual EnvironmentParams and the
// properties of the EnvConfigFactory.
func (f EnvConfigFactory) MakeEnvironmentConfig(params EnvironmentParams) config.EnvConfig {
	ret := config.EnvConfig{
		SDKKey:        params.SDKKey,
		MobileKey:     params.MobileKey,
		EnvID:         params.EnvID,
		Prefix:        maybeSubstituteEnvironmentID(f.DataStorePrefix, params.EnvID, params.Identifiers.FilterKey),
		TableName:     maybeSubstituteEnvironmentID(f.TableName, params.EnvID, params.Identifiers.FilterKey),
		AllowedOrigin: f.AllowedOrigin,
		AllowedHeader: f.AllowedHeader,
		SecureMode:    params.SecureMode,
		FilterKey:     params.Identifiers.FilterKey,
		Offline:       params.Offline,
	}
	if params.TTL != 0 {
		ret.TTL = ct.NewOptDuration(params.TTL)
	}

	return ret
}

func maybeSubstituteEnvironmentID(s string, envID config.EnvironmentID, filterKey config.FilterKey) string {
	id := string(envID)
	if filterKey != "" {
		id = id + "." + string(filterKey)
	}
	return strings.ReplaceAll(s, config.AutoConfigEnvironmentIDPlaceholder, id)
}
