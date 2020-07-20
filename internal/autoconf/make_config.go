package autoconf

import (
	"strings"
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
)

func CreateEnvironmentConfig(rep AutoConfigEnvironmentRep, mainConfig config.MainExtendedConfig) config.EnvConfig {
	ret := config.EnvConfig{
		SDKKey:        config.SDKKey(rep.SDKKey),
		MobileKey:     config.MobileKey(rep.MobKey),
		EnvID:         config.EnvironmentID(rep.EnvID),
		Prefix:        maybeSubstituteEnvironmentID(mainConfig.EnvDatastorePrefix, rep.EnvID),
		TableName:     maybeSubstituteEnvironmentID(mainConfig.EnvDatastoreTableName, rep.EnvID),
		AllowedOrigin: mainConfig.EnvAllowedOrigin,
		SecureMode:    rep.SecureMode,
	}
	if rep.DefaultTTL != 0 {
		ret.TTL = ct.NewOptDuration(time.Duration(rep.DefaultTTL) * time.Minute)
	}
	return ret
}

func maybeSubstituteEnvironmentID(s, envID string) string {
	return strings.ReplaceAll(s, config.AutoConfigEnvironmentIDPlaceholder, envID)
}
