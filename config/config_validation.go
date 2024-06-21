package config

import (
	"errors"
	"fmt"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

var (
	errTLSEnabledWithoutCertOrKey      = errors.New("TLS cert and key are required if TLS is enabled")
	errAutoConfPropertiesWithNoKey     = errors.New("must specify auto-configuration key if other auto-configuration properties are set")
	errAutoConfWithEnvironments        = errors.New("cannot configure specific environments if auto-configuration is enabled")
	errFileDataWithAutoConf            = errors.New("cannot specify both auto-configuration key and file data source")
	errOfflineModePropertiesWithNoFile = errors.New("must specify offline mode filename if other offline mode properties are set")
	errOfflineModeWithEnvironments     = errors.New("cannot configure specific environments if offline mode is enabled")
	errAutoConfWithoutDBDisambig       = errors.New(`when using auto-configuration with database storage, database prefix (or,` +
		` if using DynamoDB, table name) must be specified and must contain "` + AutoConfigEnvironmentIDPlaceholder + `"`)
	errRedisURLWithHostAndPort                 = errors.New("please specify Redis URL or host/port, but not both")
	errRedisBadHostname                        = errors.New("invalid Redis hostname")
	errConsulTokenAndTokenFile                 = errors.New("Consul token must be specified as either an inline value or a file, but not both") //nolint:stylecheck
	errAutoConfWithFilters                     = errors.New("cannot configure filters if auto-configuration is enabled")
	errMissingProjKey                          = errors.New("when filters are configured, all environments must specify a 'projKey'")
	errInvalidFileDataSourceMonitoringInterval = fmt.Errorf("file data source monitoring interval must be >= %s", minimumFileDataSourceMonitoringInterval)
	errInvalidCredentialCleanupInterval        = fmt.Errorf("credential cleanup interval must be >= %s", minimumCredentialCleanupInterval)
)

func errEnvironmentWithNoSDKKey(envName string) error {
	return fmt.Errorf("SDK key is required for environment %q", envName)
}

func errMultipleDatabases(databases []string) error {
	return fmt.Errorf("multiple databases are enabled (%s); only one is allowed", strings.Join(databases, ", "))
}

func errEnvWithoutDBDisambiguation(envName string, canUseTableName bool) error {
	if canUseTableName {
		return fmt.Errorf("environment %q does not have a prefix or table name specified for database storage", envName)
	}
	return fmt.Errorf("environment %q does not have a prefix specified for database storage", envName)
}

func errFilterUnknownProject(projKey string) error {
	return fmt.Errorf("filters are configured for project '%s', but no environment references that project", projKey)
}

func errFilterEmptyKeys(projKey string) error {
	return fmt.Errorf("filter key list for project '%s' cannot be empty", projKey)
}

func errFilterInvalidKey(projKey string, i int) error {
	return fmt.Errorf("filter key [%d] for project '%s' is malformed (note: lists are comma-delimited)", i, projKey)
}

func warnEnvWithoutDBDisambiguation(envName string, canUseTableName bool) string {
	return errEnvWithoutDBDisambiguation(envName, canUseTableName).Error() +
		"; this would be an error if multiple environments were configured"
}

// ValidateConfig ensures that the configuration does not contain contradictory properties.
//
// This method covers validation rules that can't be enforced on a per-field basis (for instance, if
// either field A or field B can be specified but it's invalid to specify both). It is allowed to modify
// the Config struct in order to canonicalize settings in a way that simplifies things for the Relay code
// (for instance, converting Redis host/port settings into a Redis URL, or converting deprecated fields to
// non-deprecated ones).
//
// LoadConfigFromEnvironment and LoadConfigFromFile both call this method as a last step, but it is also
// called again by the Relay constructor because it is possible for application code that uses Relay as a
// library to construct a Config programmatically.
func ValidateConfig(c *Config, loggers ldlog.Loggers) error {
	var result ct.ValidationResult

	validateConfigDefaultURLs(c)
	validateConfigTLS(&result, c)
	validateConfigEnvironments(&result, c)
	validateConfigDatabases(&result, c, loggers)
	validateConfigFilters(&result, c)
	validateOfflineMode(&result, c)
	validateCredentialCleanupInterval(&result, c)

	return result.GetError()
}

func validateConfigDefaultURLs(c *Config) {
	switch {
	case !c.Main.BaseURI.IsDefined(),
		*c.Main.BaseURI.Get() == *defaultBaseURI.Get(),
		*c.Main.BaseURI.Get() == *oldDefaultBaseURI.Get():
		c.Main.BaseURI = defaultBaseURI
		if !c.Main.ClientSideBaseURI.IsDefined() {
			c.Main.ClientSideBaseURI = defaultClientSideBaseURI
		}
	default:
		// BaseuRI was set to some custom value, which may mean the customer has a private
		// instance. In that case, the default for ClientSideBaseURI if not specified should be to
		// make it the same as BaseURI.
		if !c.Main.ClientSideBaseURI.IsDefined() {
			c.Main.ClientSideBaseURI = c.Main.BaseURI
		}
	}
	if !c.Main.StreamURI.IsDefined() {
		c.Main.StreamURI = defaultStreamURI
	}
	if !c.Events.EventsURI.IsDefined() {
		c.Events.EventsURI = defaultEventsURI
	}
}

func validateConfigTLS(result *ct.ValidationResult, c *Config) {
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		result.AddError(nil, errTLSEnabledWithoutCertOrKey)
	}
}

func validateConfigEnvironments(result *ct.ValidationResult, c *Config) {
	if c.AutoConfig.Key == "" {
		if c.AutoConfig.EnvDatastorePrefix != "" || c.AutoConfig.EnvDatastoreTableName != "" ||
			len(c.AutoConfig.EnvAllowedOrigin.Values()) != 0 || len(c.AutoConfig.EnvAllowedHeader.Values()) != 0 {
			result.AddError(nil, errAutoConfPropertiesWithNoKey)
		}
	} else if len(c.Environment) != 0 {
		result.AddError(nil, errAutoConfWithEnvironments)
	}
	if c.OfflineMode.FileDataSource == "" {
		if c.OfflineMode.EnvDatastorePrefix != "" || c.OfflineMode.EnvDatastoreTableName != "" ||
			len(c.OfflineMode.EnvAllowedOrigin.Values()) != 0 || len(c.OfflineMode.EnvAllowedHeader.Values()) != 0 || c.OfflineMode.FileDataSourceMonitoringInterval.IsDefined() {
			result.AddError(nil, errOfflineModePropertiesWithNoFile)
		}
	} else {
		if c.AutoConfig.Key != "" {
			result.AddError(nil, errFileDataWithAutoConf)
		}
		if len(c.Environment) != 0 {
			result.AddError(nil, errOfflineModeWithEnvironments)
		}
	}

	for envName, envConfig := range c.Environment {
		if envConfig.SDKKey == "" {
			result.AddError(nil, errEnvironmentWithNoSDKKey(envName))
		}
	}
}

func validateConfigFilters(result *ct.ValidationResult, c *Config) {
	if len(c.Filters) == 0 {
		return
	}
	// If Auto Config is enabled, then filters will have no effect and should cause an error.
	if c.AutoConfig.Key != "" {
		result.AddError(nil, errAutoConfWithFilters)
		return
	}
	for _, proj := range c.Environment {
		if proj.ProjKey == "" {
			result.AddError(nil, errMissingProjKey)
			return
		}
	}
	for projKey, conf := range c.Filters {
		// For every project key defined by a [filter] section,
		// that project key must be referenced by at least one environment.
		foundProj := false
		for _, e := range c.Environment {
			if e.ProjKey == projKey {
				foundProj = true
				break
			}
		}
		if !foundProj {
			result.AddError(nil, errFilterUnknownProject(projKey))
			continue
		}

		// The list of filter keys cannot be empty
		if len(conf.Keys.Values()) == 0 {
			result.AddError(nil, errFilterEmptyKeys(projKey))
		} else {
			// Filter keys cannot be empty strings
			for i, k := range conf.Keys.Values() {
				if k == "" {
					result.AddError(nil, errFilterInvalidKey(projKey, i))
				}
			}
		}
	}
}

func validateOfflineMode(result *ct.ValidationResult, c *Config) {
	if c.OfflineMode.FileDataSourceMonitoringInterval.IsDefined() {
		interval := c.OfflineMode.FileDataSourceMonitoringInterval.GetOrElse(0)
		if interval < minimumFileDataSourceMonitoringInterval {
			result.AddError(nil, errInvalidFileDataSourceMonitoringInterval)
		}
	}
}

func validateCredentialCleanupInterval(result *ct.ValidationResult, c *Config) {
	if c.Main.CredentialCleanupInterval.IsDefined() {
		interval := c.Main.CredentialCleanupInterval.GetOrElse(0)
		if interval < minimumCredentialCleanupInterval {
			result.AddError(nil, errInvalidCredentialCleanupInterval)
		}
	}
}
func validateConfigDatabases(result *ct.ValidationResult, c *Config, loggers ldlog.Loggers) {
	normalizeRedisConfig(result, c)

	databases := []string{}
	if c.Redis.URL.IsDefined() {
		databases = append(databases, "Redis")
	}
	if c.Consul.Host != "" {
		databases = append(databases, "Consul")
	}
	if c.DynamoDB.Enabled {
		databases = append(databases, "DynamoDB")
	}

	if len(databases) == 0 {
		return
	}
	if len(databases) > 1 {
		result.AddError(nil, errMultipleDatabases(databases))
		return // no point doing further database config validation if it's in this state
	}

	if c.Consul.Host != "" {
		if c.Consul.Token != "" && c.Consul.TokenFile != "" {
			result.AddError(nil, errConsulTokenAndTokenFile)
		}
	}

	// When using a database, if there is more than one environment configured, they must be distinguished by
	// different prefixes (or, when using DynamoDB, you can use different table names). In auto-config mode,
	// we must assume that there are multiple environments.
	switch {
	case len(c.Environment) == 1:
		for name, e := range c.Environment {
			if e.Prefix == "" && !(c.DynamoDB.Enabled && e.TableName != "") {
				loggers.Warn(warnEnvWithoutDBDisambiguation(name, c.DynamoDB.Enabled))
			}
		}

	case len(c.Environment) > 1:
		for name, e := range c.Environment {
			if e.Prefix == "" && !(c.DynamoDB.Enabled && e.TableName != "") {
				result.AddError(nil, errEnvWithoutDBDisambiguation(name, c.DynamoDB.Enabled))
			}
		}

	case c.AutoConfig.Key.Defined():
		// Same as previous case, except that in auto-config mode we must assume that there are multiple environments.
		if !strings.Contains(c.AutoConfig.EnvDatastorePrefix, AutoConfigEnvironmentIDPlaceholder) &&
			!(c.DynamoDB.Enabled && strings.Contains(c.AutoConfig.EnvDatastoreTableName, AutoConfigEnvironmentIDPlaceholder)) {
			result.AddError(nil, errAutoConfWithoutDBDisambig)
		}
	}
}

func normalizeRedisConfig(result *ct.ValidationResult, c *Config) {
	if c.Redis.URL.IsDefined() {
		if c.Redis.Host != "" || c.Redis.Port.IsDefined() {
			result.AddError(nil, errRedisURLWithHostAndPort)
		}
	} else if c.Redis.Host != "" || c.Redis.Port.IsDefined() {
		host := c.Redis.Host
		if host == "" {
			host = defaultRedisHost
		}
		port := c.Redis.Port.GetOrElse(defaultRedisPort)
		url, err := ct.NewOptURLAbsoluteFromString(fmt.Sprintf("redis://%s:%d", host, port))
		if err != nil {
			result.AddError(nil, errRedisBadHostname)
		}
		c.Redis.URL = url
		c.Redis.Host = ""
		c.Redis.Port = ct.OptIntGreaterThanZero{}
	}
}
