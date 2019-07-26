package relay

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/launchdarkly/gcfg"
	"gopkg.in/launchdarkly/ld-relay.v5/httpconfig"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/metrics"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

const (
	defaultRedisLocalTtlMs       = 30000
	defaultPort                  = 8030
	defaultAllowedOrigin         = "*"
	defaultEventCapacity         = 1000
	defaultEventsUri             = "https://events.launchdarkly.com"
	defaultBaseUri               = "https://app.launchdarkly.com"
	defaultStreamUri             = "https://stream.launchdarkly.com"
	defaultHeartbeatIntervalSecs = 180
	defaultMetricsPrefix         = "launchdarkly_relay"
	defaultFlushIntervalSecs     = 5

	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

var (
	uuidHeaderPattern = regexp.MustCompile(`^(?:api_key )?((?:[a-z]{3}-)?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89aAbB][a-f0-9]{3}-[a-f0-9]{12})$`)
)

// Config describes the configuration for a relay instance.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
type Config struct {
	Main        MainConfig
	Events      events.Config
	Redis       RedisConfig
	Consul      ConsulConfig
	DynamoDB    DynamoDBConfig
	Environment map[string]*EnvConfig
	Proxy       httpconfig.ProxyConfig
	MetricsConfig
}

// MainConfig contains global configuration options for Relay.
//
// This corresponds to the [Main] section in the configuration file.
type MainConfig struct {
	ExitOnError            bool
	IgnoreConnectionErrors bool
	StreamUri              string
	BaseUri                string
	Port                   int
	HeartbeatIntervalSecs  int
	TLSEnabled             bool
	TLSCert                string
	TLSKey                 string
}

// RedisConfig configures the optional Redis integration, which is used only if Host is non-empty.
//
// This corresponds to the [Redis] section in the configuration file.
type RedisConfig struct {
	Host     string
	Port     int
	Url      string
	LocalTtl int
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
type DatadogConfig struct {
	TraceAddr *string
	StatsAddr *string
	Tag       []string
	CommonMetricsConfig
}

func (c DatadogConfig) toOptions() metrics.ExporterOptions {
	return metrics.DatadogOptions{
		TraceAddr: c.TraceAddr,
		StatsAddr: c.StatsAddr,
		Tags:      c.Tag,
		Prefix:    c.getPrefix(),
	}
}

// StackdriverConfig configures the optional Stackdriver integration, which is used only if Enabled is true.
type StackdriverConfig struct {
	ProjectID string
	CommonMetricsConfig
}

func (c StackdriverConfig) toOptions() metrics.ExporterOptions {
	return metrics.StackdriverOptions{
		ProjectID: c.ProjectID,
		Prefix:    c.getPrefix(),
	}
}

// PrometheusConfig configures the optional Prometheus integration, which is used only if Enabled is true.
type PrometheusConfig struct {
	Port int
	CommonMetricsConfig
}

func (c PrometheusConfig) toOptions() (options metrics.ExporterOptions) {
	return metrics.PrometheusOptions{
		Port:   c.Port,
		Prefix: c.getPrefix(),
	}
}

// ExporterConfig is used internally to hold options for metrics integrations.
type ExporterConfig interface {
	toOptions() metrics.ExporterOptions
	enabled() bool
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

func (c MetricsConfig) toOptions() (options []metrics.ExporterOptions) {
	exporterConfigs := []ExporterConfig{c.Datadog, c.Stackdriver, c.Prometheus}
	for _, e := range exporterConfigs {
		if e.enabled() {
			options = append(options, e.toOptions())
		}
	}
	return options
}

// DefaultConfig contains defaults for all relay configuration sections.
//
// If you are incorporating Relay into your own code and configuring it programmatically, it is best to
// start by copying relay.DefaultConfig and then changing only the fields you need to change.
var DefaultConfig = Config{
	Main: MainConfig{
		BaseUri:               defaultBaseUri,
		StreamUri:             defaultStreamUri,
		HeartbeatIntervalSecs: defaultHeartbeatIntervalSecs,
		Port: defaultPort,
	},
	Events: events.Config{
		Capacity:          defaultEventCapacity,
		EventsUri:         defaultEventsUri,
		FlushIntervalSecs: defaultFlushIntervalSecs,
	},
	Redis: RedisConfig{
		LocalTtl: defaultRedisLocalTtlMs,
	},
}

// LoadConfigFile reads a configuration file into a Config struct and performs basic validation.
func LoadConfigFile(c *Config, path string) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return fmt.Errorf(`failed to read configuration file "%s": %s`, path, err)
	}

	for envName, envConfig := range c.Environment {
		if envConfig.ApiKey != "" {
			if envConfig.SdkKey == "" {
				envConfig.SdkKey = envConfig.ApiKey
				c.Environment[envName] = envConfig
				logging.Warning.Println(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				logging.Warning.Println(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
	}

	return validateConfig(c)
}

func validateConfig(c *Config) error {
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		return errors.New("tlsCert and tlsKey are required if TLS is enabled")
	}
	return nil
}
