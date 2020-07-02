package config

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	defaultDatabaseLocalTTLMs    = 30000
	defaultPort                  = 8030
	defaultEventCapacity         = 1000
	defaultEventsURI             = "https://events.launchdarkly.com"
	defaultBaseURI               = "https://app.launchdarkly.com"
	defaultStreamURI             = "https://stream.launchdarkly.com"
	defaultHeartbeatIntervalSecs = 180
	defaultMetricsPrefix         = "launchdarkly_relay"
	defaultFlushIntervalSecs     = 5
	defaultRedisPort             = 6379
	defaultPrometheusPort        = 8031
)

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
	StreamUri              string
	BaseUri                string
	Port                   int
	HeartbeatIntervalSecs  int
	TLSEnabled             bool
	TLSCert                string
	TLSKey                 string
	LogLevel               string
}

func (c MainConfig) GetLogLevel() ldlog.LogLevel {
	level, _ := getLogLevelByName(c.LogLevel)
	return level
}

// EventsConfig contains configuration parameters for proxying events.
type EventsConfig struct {
	EventsUri         string
	SendEvents        bool
	FlushIntervalSecs int
	SamplingInterval  int32
	Capacity          int
	InlineUsers       bool
}

// RedisConfig configures the optional Redis integration, which is used only if Host is non-empty.
//
// This corresponds to the [Redis] section in the configuration file.
type RedisConfig struct {
	Host     string
	Port     int
	Url      string
	LocalTtl int
	Tls      bool
	Password string
}

// ConsulConfig configures the optional Consul integration, which is used only if Host is non-empty.
//
// This corresponds to the [Consul] section in the configuration file.
type ConsulConfig struct {
	Host     string
	LocalTtl int
}

// DynamoDBConfig configures the optional DynamoDB integration, which is used only if Enabled is true.
//
// This corresponds to the [DynamoDB] section in the configuration file.
type DynamoDBConfig struct {
	Enabled   bool
	TableName string
	Url       string
	LocalTtl  int
}

// EnvConfig describes an environment to be relayed. There may be any number of these.
//
// This corresponds to one of the [environment "env-name"] sections in the configuration file. In the
// Config.Environment map, each key is an environment name and each value is an EnvConfig.
type EnvConfig struct {
	SdkKey             string
	ApiKey             string // deprecated, equivalent to SdkKey
	MobileKey          *string
	EnvId              *string
	Prefix             string // used only if Redis, Consul, or DynamoDB is enabled
	TableName          string // used only if DynamoDB is enabled
	AllowedOrigin      *[]string
	InsecureSkipVerify bool
	LogLevel           string
	TtlMinutes         int
}

func (c EnvConfig) GetLogLevel() ldlog.LogLevel {
	level, _ := getLogLevelByName(c.LogLevel)
	return level
}

// ProxyConfig represents all the supported proxy options.
type ProxyConfig struct {
	Url         string
	NtlmAuth    bool
	User        string
	Password    string
	Domain      string
	CaCertFiles string
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
	TraceAddr *string
	StatsAddr *string
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

func (c CommonMetricsConfig) getPrefix() string {
	prefix := c.Prefix
	if prefix == "" {
		return defaultMetricsPrefix
	}
	return prefix
}

func (c CommonMetricsConfig) enabled() bool {
	return c.Enabled
}

// DefaultConfig contains defaults for all relay configuration sections.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
var DefaultConfig = Config{
	Main: MainConfig{
		BaseUri:               defaultBaseURI,
		StreamUri:             defaultStreamURI,
		HeartbeatIntervalSecs: defaultHeartbeatIntervalSecs,
		Port:                  defaultPort,
	},
	Events: EventsConfig{
		Capacity:          defaultEventCapacity,
		EventsUri:         defaultEventsURI,
		FlushIntervalSecs: defaultFlushIntervalSecs,
	},
	Redis: RedisConfig{
		LocalTtl: defaultDatabaseLocalTTLMs,
	},
	Consul: ConsulConfig{
		LocalTtl: defaultDatabaseLocalTTLMs,
	},
	DynamoDB: DynamoDBConfig{
		LocalTtl: defaultDatabaseLocalTTLMs,
	},
	MetricsConfig: MetricsConfig{
		Prometheus: PrometheusConfig{
			Port: defaultPrometheusPort,
		},
	},
}
