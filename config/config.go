package config

import (
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
)

const (
	// DefaultBaseURI is the default value for the base URI of LaunchDarkly services (polling endpoints).
	DefaultBaseURI = "https://app.launchdarkly.com"

	// DefaultStreamURI is the default value for the base URI of LaunchDarkly services (streaming endpoints).
	DefaultStreamURI = "https://stream.launchdarkly.com"

	// DefaultEventsURI is the default value for the base URI of LaunchDarkly services (event endpoints).
	DefaultEventsURI = "https://events.launchdarkly.com"

	// DefaultHeartbeatInterval is the default value for MainConfig.HeartBeatInterval if not specified.
	DefaultHeartbeatInterval = time.Minute * 3

	// DefaultEventsFlushInterval is the default value for EventsConfig.FlushInterval if not specified.
	DefaultEventsFlushInterval = time.Second * 5

	// DefaultDatabaseCacheTTL is the default value for the LocalTTL parameter for databases if not specified.
	DefaultDatabaseCacheTTL = time.Second * 30
)

const (
	defaultPort           = 8030
	defaultEventCapacity  = 1000
	defaultRedisHost      = "localhost"
	defaultRedisPort      = 6379
	defaultConsulHost     = "localhost"
	defaultPrometheusPort = 8031
)

var (
	defaultRedisURL = newOptURLAbsoluteMustBeValid("redis://localhost:6379")
)

// DefaultLoggers is the default logging configuration used by Relay.
//
// Output goes to stdout, except Error level which goes to stderr. Debug level is disabled.
var DefaultLoggers = logging.MakeDefaultLoggers()

// Config describes the configuration for a relay instance.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
type Config struct {
	Main        MainConfig
	Events      EventsConfig
	Redis       RedisConfig
	Consul      ConsulConfig
	DynamoDB    DynamoDBConfig
	Environment map[string]*EnvConfig
	Proxy       ProxyConfig
	MetricsConfig
}

// MainConfig contains global configuration options for Relay.
//
// This corresponds to the [Main] section in the configuration file.
type MainConfig struct {
	ExitOnError             bool              `conf:"EXIT_ON_ERROR"`
	ExitAlways              bool              `conf:"EXIT_ALWAYS"`
	IgnoreConnectionErrors  bool              `conf:"IGNORE_CONNECTION_ERRORS"`
	StreamURI               ct.OptURLAbsolute `conf:"STREAM_URI"`
	BaseURI                 ct.OptURLAbsolute `conf:"BASE_URI"`
	Port                    int               `conf:"PORT"`
	HeartbeatInterval       ct.OptDuration    `conf:"HEARTBEAT_INTERVAL"`
	MaxClientConnectionTime ct.OptDuration    `conf:"MAX_CLIENT_CONNECTION_TIME"`
	TLSEnabled              bool              `conf:"TLS_ENABLED"`
	TLSCert                 string            `conf:"TLS_CERT"`
	TLSKey                  string            `conf:"TLS_KEY"`
	LogLevel                OptLogLevel       `conf:"LOG_LEVEL"`
}

// EventsConfig contains configuration parameters for proxying events.
type EventsConfig struct {
	EventsURI        ct.OptURLAbsolute `conf:"EVENTS_HOST"`
	SendEvents       bool              `conf:"USE_EVENTS"`
	FlushInterval    ct.OptDuration    `conf:"EVENTS_FLUSH_INTERVAL"`
	SamplingInterval int32
	Capacity         int  `conf:"EVENTS_CAPACITY"`
	InlineUsers      bool `conf:"EVENTS_INLINE_USERS"`
}

// RedisConfig configures the optional Redis integration.
//
// Redis is enabled if URL or Host is non-empty or if Port is non-zero. If only Host or Port is set,
// the other value is set to defaultRedisPort or defaultRedisHost. It is an error to set Host or
// Port if URL is also set.
//
// This corresponds to the [Redis] section in the configuration file.
type RedisConfig struct {
	Host     string `conf:"REDIS_HOST"`
	Port     int
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
type ConsulConfig struct {
	Host     string         `conf:"CONSUL_HOST"`
	LocalTTL ct.OptDuration `conf:"CACHE_TTL"`
}

// DynamoDBConfig configures the optional DynamoDB integration, which is used only if Enabled is true.
//
// This corresponds to the [DynamoDB] section in the configuration file.
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
type EnvConfig struct {
	SDKKey             SDKKey           // set from env var LD_ENV_envname
	MobileKey          MobileKey        `conf:"LD_MOBILE_KEY_"`
	EnvID              EnvironmentID    `conf:"LD_CLIENT_SIDE_ID_"`
	Prefix             string           `conf:"LD_PREFIX_"`     // used only if Redis, Consul, or DynamoDB is enabled
	TableName          string           `conf:"LD_TABLE_NAME_"` // used only if DynamoDB is enabled
	AllowedOrigin      ct.OptStringList `conf:"LD_ALLOWED_ORIGIN_"`
	SecureMode         bool             `conf:"LD_SECURE_MODE_"`
	InsecureSkipVerify bool             // deliberately not settable by env vars
	LogLevel           OptLogLevel      `conf:"LD_LOG_LEVEL_"`
	TTL                ct.OptDuration   `conf:"LD_TTL_"`
}

// ProxyConfig represents all the supported proxy options.
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

// CommonMetricsConfig contains fields that are common to DatadogCOnfig, StackdriverConfig, and PrometheusConfig.
type CommonMetricsConfig struct {
	Enabled bool
	Prefix  string
}

// DatadogConfig configures the optional Datadog integration, which is used only if Enabled is true.
//
// This corresponds to the [Datadog] section in the configuration file.
type DatadogConfig struct {
	TraceAddr string `conf:"DATADOG_TRACE_ADDR"`
	StatsAddr string `conf:"DATADOG_STATS_ADDR"`
	Tag       []string
	CommonMetricsConfig
}

// StackdriverConfig configures the optional Stackdriver integration, which is used only if Enabled is true.
//
// This corresponds to the [StackdriverConfig] section in the configuration file.
type StackdriverConfig struct {
	ProjectID string `conf:"STACKDRIVER_PROJECT_ID"`
	CommonMetricsConfig
}

// PrometheusConfig configures the optional Prometheus integration, which is used only if Enabled is true.
//
// This corresponds to the [PrometheusConfig] section in the configuration file.
type PrometheusConfig struct {
	Port int `conf:"PROMETHEUS_PORT"`
	CommonMetricsConfig
}

// DefaultConfig contains defaults for all relay configuration sections.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
var DefaultConfig = Config{
	Main: MainConfig{
		BaseURI:   newOptURLAbsoluteMustBeValid(DefaultBaseURI),
		StreamURI: newOptURLAbsoluteMustBeValid(DefaultStreamURI),
		Port:      defaultPort,
	},
	Events: EventsConfig{
		Capacity:  defaultEventCapacity,
		EventsURI: newOptURLAbsoluteMustBeValid(DefaultEventsURI),
	},
	MetricsConfig: MetricsConfig{
		Prometheus: PrometheusConfig{
			Port: defaultPrometheusPort,
		},
	},
}
