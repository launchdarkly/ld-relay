package config

import (
	"time"

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
	defaultRedisURL = newOptAbsoluteURLMustBeValid("redis://localhost:6379")
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
	ExitOnError            bool
	ExitAlways             bool
	IgnoreConnectionErrors bool
	StreamURI              OptAbsoluteURL
	BaseURI                OptAbsoluteURL
	Port                   int
	HeartbeatInterval      OptDuration
	TLSEnabled             bool
	TLSCert                string
	TLSKey                 string
	LogLevel               OptLogLevel
}

// EventsConfig contains configuration parameters for proxying events.
type EventsConfig struct {
	EventsURI        OptAbsoluteURL
	SendEvents       bool
	FlushInterval    OptDuration
	SamplingInterval int32
	Capacity         int
	InlineUsers      bool
}

// RedisConfig configures the optional Redis integration.
//
// Redis is enabled if URL or Host is non-empty or if Port is non-zero. If only Host or Port is set,
// the other value is set to defaultRedisPort or defaultRedisHost. It is an error to set Host or
// Port if URL is also set.
//
// This corresponds to the [Redis] section in the configuration file.
type RedisConfig struct {
	Host     string
	Port     int
	URL      OptAbsoluteURL
	LocalTTL OptDuration
	TLS      bool
	Password string
}

// ConsulConfig configures the optional Consul integration.
//
// Consul is enabled if Host is non-empty.
//
// This corresponds to the [Consul] section in the configuration file.
type ConsulConfig struct {
	Host     string
	LocalTTL OptDuration
}

// DynamoDBConfig configures the optional DynamoDB integration, which is used only if Enabled is true.
//
// This corresponds to the [DynamoDB] section in the configuration file.
type DynamoDBConfig struct {
	Enabled   bool
	TableName string
	URL       OptAbsoluteURL
	LocalTTL  OptDuration
}

// EnvConfig describes an environment to be relayed. There may be any number of these.
//
// This corresponds to one of the [environment "env-name"] sections in the configuration file. In the
// Config.Environment map, each key is an environment name and each value is an EnvConfig.
type EnvConfig struct {
	SDKKey             SDKKey
	APIKey             string // deprecated, equivalent to SdkKey
	MobileKey          MobileKey
	EnvID              EnvironmentID
	Prefix             string // used only if Redis, Consul, or DynamoDB is enabled
	TableName          string // used only if DynamoDB is enabled
	AllowedOrigin      []string
	InsecureSkipVerify bool
	LogLevel           OptLogLevel
	TTL                OptDuration
}

// ProxyConfig represents all the supported proxy options.
type ProxyConfig struct {
	URL         OptAbsoluteURL
	NTLMAuth    bool
	User        string
	Password    string
	Domain      string
	CACertFiles string
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
	TraceAddr string
	StatsAddr string
	Tag       []string
	CommonMetricsConfig
}

// StackdriverConfig configures the optional Stackdriver integration, which is used only if Enabled is true.
//
// This corresponds to the [StackdriverConfig] section in the configuration file.
type StackdriverConfig struct {
	ProjectID string
	CommonMetricsConfig
}

// PrometheusConfig configures the optional Prometheus integration, which is used only if Enabled is true.
//
// This corresponds to the [PrometheusConfig] section in the configuration file.
type PrometheusConfig struct {
	Port int
	CommonMetricsConfig
}

// DefaultConfig contains defaults for all relay configuration sections.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
var DefaultConfig = Config{
	Main: MainConfig{
		BaseURI:   newOptAbsoluteURLMustBeValid(DefaultBaseURI),
		StreamURI: newOptAbsoluteURLMustBeValid(DefaultStreamURI),
		Port:      defaultPort,
	},
	Events: EventsConfig{
		Capacity:  defaultEventCapacity,
		EventsURI: newOptAbsoluteURLMustBeValid(DefaultEventsURI),
	},
	MetricsConfig: MetricsConfig{
		Prometheus: PrometheusConfig{
			Port: defaultPrometheusPort,
		},
	},
}
