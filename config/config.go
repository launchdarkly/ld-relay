package config

import (
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/logging"
)

const (
	// DefaultPort is the port that Relay runs on if not otherwise specified.
	DefaultPort = 8030

	// DefaultBaseURI is the default value for the base URI of LaunchDarkly services (polling endpoints).
	DefaultBaseURI = "https://app.launchdarkly.com"

	// DefaultStreamURI is the default value for the base URI of LaunchDarkly services (streaming endpoints).
	DefaultStreamURI = "https://stream.launchdarkly.com"

	// DefaultEventsURI is the default value for the base URI of LaunchDarkly services (event endpoints).
	DefaultEventsURI = "https://events.launchdarkly.com"

	// DefaultEventCapacity is the default value for EventsConfig.Capacity if not specified.
	DefaultEventCapacity = 1000

	// DefaultHeartbeatInterval is the default value for MainConfig.HeartBeatInterval if not specified.
	DefaultHeartbeatInterval = time.Minute * 3

	// DefaultEventsFlushInterval is the default value for EventsConfig.FlushInterval if not specified.
	DefaultEventsFlushInterval = time.Second * 5

	// DefaultDisconnectedStatusTime is the default value for MainConfig.DisconnectedStatusTime if not specified.
	DefaultDisconnectedStatusTime = time.Minute

	// DefaultDatabaseCacheTTL is the default value for the LocalTTL parameter for databases if not specified.
	DefaultDatabaseCacheTTL = time.Second * 30

	// DefaultPrometheusPort is the default value for PrometheusConfig.Port if not specified.
	DefaultPrometheusPort = 8031

	// AutoConfigEnvironmentIDPlaceholder is a string that can appear within
	// AutoConfigConfig.EnvDataStorePrefix or AutoConfigConfig.EnvDataStoreTableName to indicate that
	// the environment ID should be substituted at that point.
	//
	// For instance, if EnvDataStorePrefix is "LD-$CID", the value of that setting for an environment
	// whose ID is "12345" would be "LD-12345".
	AutoConfigEnvironmentIDPlaceholder = "$CID"
)

const (
	defaultRedisHost  = "localhost"
	defaultRedisPort  = 6379
	defaultConsulHost = "localhost"
)

var (
	defaultRedisURL, _ = ct.NewOptURLAbsoluteFromString("redis://localhost:6379") //nolint:gochecknoglobals
)

// DefaultLoggers is the default logging configuration used by Relay.
//
// Output goes to stdout, except Error level which goes to stderr. Debug level is disabled.
var DefaultLoggers = logging.MakeDefaultLoggers() //nolint:gochecknoglobals

// Config describes the configuration for a relay instance.
//
// Some fields use special types that enforce validation rules, such as URL fields which must
// be absolute URLs, port numbers which must be greater than zero, or durations. This validation
// is done automatically when reading the configuration from a file or from environment variables.
//
// If you are incorporating Relay into your own code and configuring it programmatically, you
// may need to use functions from go-configtypes such as NewOptDuration to set fields that have
// validation rules.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type Config struct {
	Main        MainConfig
	AutoConfig  AutoConfigConfig
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

// MainConfig contains global configuration options for Relay.
//
// This corresponds to the [Main] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type MainConfig struct {
	ExitOnError                 bool                     `conf:"EXIT_ON_ERROR"`
	ExitAlways                  bool                     `conf:"EXIT_ALWAYS"`
	IgnoreConnectionErrors      bool                     `conf:"IGNORE_CONNECTION_ERRORS"`
	StreamURI                   ct.OptURLAbsolute        `conf:"STREAM_URI"`
	BaseURI                     ct.OptURLAbsolute        `conf:"BASE_URI"`
	Port                        ct.OptIntGreaterThanZero `conf:"PORT"`
	HeartbeatInterval           ct.OptDuration           `conf:"HEARTBEAT_INTERVAL"`
	MaxClientConnectionTime     ct.OptDuration           `conf:"MAX_CLIENT_CONNECTION_TIME"`
	DisconnectedStatusTime      ct.OptDuration           `conf:"DISCONNECTED_STATUS_TIME"`
	DisableInternalUsageMetrics bool                     `conf:"DISABLE_INTERNAL_USAGE_METRICS"`
	TLSEnabled                  bool                     `conf:"TLS_ENABLED"`
	TLSCert                     string                   `conf:"TLS_CERT"`
	TLSKey                      string                   `conf:"TLS_KEY"`
	TLSMinVersion               OptTLSVersion            `conf:"TLS_MIN_VERSION"`
	LogLevel                    OptLogLevel              `conf:"LOG_LEVEL"`
}

// AutoConfigConfig contains configuration parameters for the auto-configuration feature.
type AutoConfigConfig struct {
	Key                   AutoConfigKey    `conf:"AUTO_CONFIG_KEY"`
	EnvDatastorePrefix    string           `conf:"ENV_DATASTORE_PREFIX"`
	EnvDatastoreTableName string           `conf:"ENV_DATASTORE_TABLE_NAME"`
	EnvAllowedOrigin      ct.OptStringList `conf:"ENV_ALLOWED_ORIGIN"`
}

// EventsConfig contains configuration parameters for proxying events.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type EventsConfig struct {
	EventsURI     ct.OptURLAbsolute        `conf:"EVENTS_HOST"`
	SendEvents    bool                     `conf:"USE_EVENTS"`
	FlushInterval ct.OptDuration           `conf:"EVENTS_FLUSH_INTERVAL"`
	Capacity      ct.OptIntGreaterThanZero `conf:"EVENTS_CAPACITY"`
	InlineUsers   bool                     `conf:"EVENTS_INLINE_USERS"`
}

// RedisConfig configures the optional Redis integration.
//
// Redis is enabled if URL or Host is non-empty or if Port is set. If only Host or Port is set,
// the other value is set to defaultRedisPort or defaultRedisHost. It is an error to set Host or
// Port if URL is also set.
//
// This corresponds to the [Redis] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type RedisConfig struct {
	Host     string `conf:"REDIS_HOST"`
	Port     ct.OptIntGreaterThanZero
	URL      ct.OptURLAbsolute `conf:"REDIS_URL"`
	LocalTTL ct.OptDuration    `conf:"CACHE_TTL"`
	TLS      bool              `conf:"REDIS_TLS"`
	Password string            `conf:"REDIS_PASSWORD"`
}

// ConsulConfig configures the optional Consul integration.
//
// Consul is enabled if Host is non-empty.
//
// This corresponds to the [Consul] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type ConsulConfig struct {
	Host     string         `conf:"CONSUL_HOST"`
	LocalTTL ct.OptDuration `conf:"CACHE_TTL"`
}

// DynamoDBConfig configures the optional DynamoDB integration, which is used only if Enabled is true.
//
// This corresponds to the [DynamoDB] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type DynamoDBConfig struct {
	Enabled   bool              `conf:"USE_DYNAMODB"`
	TableName string            `conf:"DYNAMODB_TABLE"`
	URL       ct.OptURLAbsolute `conf:"DYNAMODB_URL"`
	LocalTTL  ct.OptDuration    `conf:"CACHE_TTL"`
}

// EnvConfig describes an environment to be relayed. There may be any number of these.
//
// This corresponds to one of the [environment "env-name"] sections in the configuration file. In the
// Config.Environment map, each key is an environment name and each value is an EnvConfig.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type EnvConfig struct {
	SDKKey        SDKKey           // set from env var LD_ENV_envname
	MobileKey     MobileKey        `conf:"LD_MOBILE_KEY_"`
	EnvID         EnvironmentID    `conf:"LD_CLIENT_SIDE_ID_"`
	Prefix        string           `conf:"LD_PREFIX_"`     // used only if Redis, Consul, or DynamoDB is enabled
	TableName     string           `conf:"LD_TABLE_NAME_"` // used only if DynamoDB is enabled
	AllowedOrigin ct.OptStringList `conf:"LD_ALLOWED_ORIGIN_"`
	SecureMode    bool             `conf:"LD_SECURE_MODE_"`
	LogLevel      OptLogLevel      `conf:"LD_LOG_LEVEL_"`
	TTL           ct.OptDuration   `conf:"LD_TTL_"`
}

// ProxyConfig represents all the supported proxy options.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type ProxyConfig struct {
	URL         ct.OptURLAbsolute `conf:"PROXY_URL"`
	NTLMAuth    bool              `conf:"PROXY_AUTH_NTLM"`
	User        string            `conf:"PROXY_AUTH_USER"`
	Password    string            `conf:"PROXY_AUTH_PASSWORD"`
	Domain      string            `conf:"PROXY_AUTH_DOMAIN"`
	CACertFiles ct.OptStringList  `conf:"PROXY_CA_CERTS"`
}

// MetricsConfig contains configurations for optional metrics integrations.
//
// This corresponds to the [Datadog], [Stackdriver], and [Prometheus] sections in the configuration file.
type MetricsConfig struct {
	Datadog     DatadogConfig
	Stackdriver StackdriverConfig
	Prometheus  PrometheusConfig
}

// DatadogConfig configures the optional Datadog integration, which is used only if Enabled is true.
//
// This corresponds to the [Datadog] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type DatadogConfig struct {
	Enabled   bool     `conf:"USE_DATADOG"`
	Prefix    string   `conf:"DATADOG_PREFIX"`
	TraceAddr string   `conf:"DATADOG_TRACE_ADDR"`
	StatsAddr string   `conf:"DATADOG_STATS_ADDR"`
	Tag       []string // special handling in LoadConfigFromEnvironment
}

// StackdriverConfig configures the optional Stackdriver integration, which is used only if Enabled is true.
//
// This corresponds to the [StackdriverConfig] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type StackdriverConfig struct {
	Enabled   bool   `conf:"USE_STACKDRIVER"`
	Prefix    string `conf:"STACKDRIVER_PREFIX"`
	ProjectID string `conf:"STACKDRIVER_PROJECT_ID"`
}

// PrometheusConfig configures the optional Prometheus integration, which is used only if Enabled is true.
//
// This corresponds to the [PrometheusConfig] section in the configuration file.
//
// Since configuration options can be set either programmatically, or from a file, or from environment
// variables, individual fields are not documented here; instead, see the `README.md` section on
// configuration.
type PrometheusConfig struct {
	Enabled bool                     `conf:"USE_PROMETHEUS"`
	Prefix  string                   `conf:"PROMETHEUS_PREFIX"`
	Port    ct.OptIntGreaterThanZero `conf:"PROMETHEUS_PORT"`
}
