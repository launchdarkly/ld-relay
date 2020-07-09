package relay

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/launchdarkly/gcfg"
	"github.com/launchdarkly/ld-relay/v6/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/logging"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

const (
	defaultDatabaseLocalTTLMs    = 30000
	defaultPort                  = 8030
	defaultAllowedOrigin         = "*"
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

func (c DatadogConfig) toOptions() metrics.ExporterOptions {
	// For historical reasons, TraceAddr and StatsAddr were declared as pointers in DatadogConfig. However,
	// if Datadog is enabled they must have non-nil values, so we have to change them to strings.
	return metrics.DatadogOptions{
		TraceAddr: strPtrToString(c.TraceAddr),
		StatsAddr: strPtrToString(c.StatsAddr),
		Tags:      c.Tag,
		Prefix:    c.getPrefix(),
	}
}

// StackdriverConfig configures the optional Stackdriver integration, which is used only if Enabled is true.
//
// This corresponds to the [StackdriverConfig] section in the configuration file.
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
//
// This corresponds to the [PrometheusConfig] section in the configuration file.
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
		BaseUri:               defaultBaseURI,
		StreamUri:             defaultStreamURI,
		HeartbeatIntervalSecs: defaultHeartbeatIntervalSecs,
		Port:                  defaultPort,
	},
	Events: events.Config{
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

// LoadConfigFile reads a configuration file into a Config struct and performs basic validation.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFile(c *Config, path string) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return fmt.Errorf(`failed to read configuration file "%s": %s`, path, err)
	}

	for envName, envConfig := range c.Environment {
		if envConfig.ApiKey != "" {
			if envConfig.SdkKey == "" {
				envConfig.SdkKey = envConfig.ApiKey
				c.Environment[envName] = envConfig
				logging.GlobalLoggers.Warn(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				logging.GlobalLoggers.Warn(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
	}

	return ValidateConfig(c)
}

// ValidateConfig ensures that the configuration does not contain contradictory properties.
func ValidateConfig(c *Config) error {
	if c.Main.TLSEnabled && (c.Main.TLSCert == "" || c.Main.TLSKey == "") {
		return errors.New("TLS cert and key are required if TLS is enabled")
	}
	if _, ok := getLogLevelByName(c.Main.LogLevel); !ok {
		return fmt.Errorf(`Invalid log level "%s"`, c.Main.LogLevel)
	}
	for _, ec := range c.Environment {
		if _, ok := getLogLevelByName(ec.LogLevel); !ok {
			return fmt.Errorf(`Invalid environment log level "%s"`, ec.LogLevel)
		}
	}
	databases := []string{}
	if c.Redis.Host != "" || c.Redis.Url != "" {
		databases = append(databases, "Redis")
	}
	if c.Consul.Host != "" {
		databases = append(databases, "Consul")
	}
	if c.DynamoDB.Enabled {
		databases = append(databases, "DynamoDB")
	}
	if len(databases) > 1 {
		return fmt.Errorf("Multiple databases are enabled (%s); only one is allowed",
			strings.Join(databases, ", "))
	}
	return nil
}

// LoadConfigFromEnvironment sets parameters in a Config struct from environment variables.
//
// The Config parameter should be initialized with default values first.
func LoadConfigFromEnvironment(c *Config) error {
	errs := make([]error, 0, 20)

	maybeSetFromEnvInt(&c.Main.Port, "PORT", &errs)
	maybeSetFromEnv(&c.Main.BaseUri, "BASE_URI")
	maybeSetFromEnv(&c.Main.StreamUri, "STREAM_URI")
	maybeSetFromEnvBool(&c.Main.ExitOnError, "EXIT_ON_ERROR")
	maybeSetFromEnvBool(&c.Main.ExitAlways, "EXIT_ALWAYS")
	maybeSetFromEnvBool(&c.Main.IgnoreConnectionErrors, "IGNORE_CONNECTION_ERRORS")
	maybeSetFromEnvInt(&c.Main.HeartbeatIntervalSecs, "HEARTBEAT_INTERVAL", &errs)
	maybeSetFromEnvBool(&c.Main.TLSEnabled, "TLS_ENABLED")
	maybeSetFromEnv(&c.Main.TLSCert, "TLS_CERT")
	maybeSetFromEnv(&c.Main.TLSKey, "TLS_KEY")
	maybeSetFromEnv(&c.Main.LogLevel, "LOG_LEVEL")

	maybeSetFromEnvBool(&c.Events.SendEvents, "USE_EVENTS")
	maybeSetFromEnv(&c.Events.EventsUri, "EVENTS_HOST")
	maybeSetFromEnvInt(&c.Events.FlushIntervalSecs, "EVENTS_FLUSH_INTERVAL", &errs)
	maybeSetFromEnvInt32(&c.Events.SamplingInterval, "EVENTS_SAMPLING_INTERVAL", &errs)
	maybeSetFromEnvInt(&c.Events.Capacity, "EVENTS_CAPACITY", &errs)
	maybeSetFromEnvBool(&c.Events.InlineUsers, "EVENTS_INLINE_USERS")

	envNames, envKeys := getEnvVarsWithPrefix("LD_ENV_")
	for _, envName := range envNames {
		ec := EnvConfig{SdkKey: envKeys[envName]}
		ec.MobileKey = maybeEnvStrPtr("LD_MOBILE_KEY_" + envName)
		ec.EnvId = maybeEnvStrPtr("LD_CLIENT_SIDE_ID_" + envName)
		maybeSetFromEnv(&ec.Prefix, "LD_PREFIX_"+envName)
		maybeSetFromEnv(&ec.TableName, "LD_TABLE_NAME_"+envName)
		maybeSetFromEnvInt(&ec.TtlMinutes, "LD_TTL_MINUTES_"+envName, &errs)
		if s := os.Getenv("LD_ALLOWED_ORIGIN_" + envName); s != "" {
			values := strings.Split(s, ",")
			ec.AllowedOrigin = &values
		}
		maybeSetFromEnv(&ec.LogLevel, "LD_LOG_LEVEL_"+envName)
		// Not supported: EnvConfig.InsecureSkipVerify (that flag should only be used for testing, never in production)
		if c.Environment == nil {
			c.Environment = make(map[string]*EnvConfig)
		}
		c.Environment[envName] = &ec
	}

	useRedis := false
	maybeSetFromEnvBool(&useRedis, "USE_REDIS")
	if useRedis || (c.Redis.Host != "" || c.Redis.Url != "") {
		host := c.Redis.Host
		portStr := ""
		if c.Redis.Port > 0 {
			portStr = fmt.Sprintf("%d", c.Redis.Port)
		}
		url := c.Redis.Url
		maybeSetFromEnv(&host, "REDIS_HOST")
		maybeSetFromEnv(&portStr, "REDIS_PORT")
		maybeSetFromEnv(&url, "REDIS_URL")
		if (host != "" || portStr != "") && url != "" {
			return errors.New("Please specify REDIS_HOST or REDIS_URL")
		}
		if url != "" {
			c.Redis.Url = url
			c.Redis.Host = ""
		} else if host != "" || portStr != "" {
			if strings.HasPrefix(portStr, "tcp://") {
				// REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a Redis container
				hostAndPort := strings.TrimPrefix(portStr, "tcp://")
				fields := strings.Split(hostAndPort, ":")
				c.Redis.Host = fields[0]
				if len(fields) > 0 {
					c.Redis.Port, _ = strconv.Atoi(fields[1])
				}
			} else {
				c.Redis.Host = host
				c.Redis.Port = defaultRedisPort
				maybeSetFromEnvInt(&c.Redis.Port, "REDIS_PORT", &errs)
				c.Redis.Url = ""
			}
		} else {
			c.Redis.Host = "localhost"
			c.Redis.Port = defaultRedisPort
		}
		maybeSetFromEnvBool(&c.Redis.Tls, "REDIS_TLS")
		maybeSetFromEnv(&c.Redis.Password, "REDIS_PASSWORD")
		maybeSetFromEnvInt(&c.Redis.LocalTtl, "REDIS_TTL", &errs)
		maybeSetFromEnvInt(&c.Redis.LocalTtl, "CACHE_TTL", &errs) // synonym
	}

	useConsul := false
	maybeSetFromEnvBool(&useConsul, "USE_CONSUL")
	if useConsul {
		c.Consul.Host = "localhost"
		maybeSetFromEnv(&c.Consul.Host, "CONSUL_HOST")
		maybeSetFromEnvInt(&c.Consul.LocalTtl, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.DynamoDB.Enabled, "USE_DYNAMODB")
	if c.DynamoDB.Enabled {
		maybeSetFromEnv(&c.DynamoDB.TableName, "DYNAMODB_TABLE")
		maybeSetFromEnv(&c.DynamoDB.Url, "DYNAMODB_URL")
		maybeSetFromEnvInt(&c.DynamoDB.LocalTtl, "CACHE_TTL", &errs)
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Datadog.Enabled, "USE_DATADOG")
	if c.MetricsConfig.Datadog.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Datadog.Prefix, "DATADOG_PREFIX")
		c.MetricsConfig.Datadog.TraceAddr = maybeEnvStrPtr("DATADOG_TRACE_ADDR")
		c.MetricsConfig.Datadog.StatsAddr = maybeEnvStrPtr("DATADOG_STATS_ADDR")
		tagNames, tagVals := getEnvVarsWithPrefix("DATADOG_TAG_")
		for _, tagName := range tagNames {
			c.MetricsConfig.Datadog.Tag = append(c.MetricsConfig.Datadog.Tag, tagName+":"+tagVals[tagName])
		}
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Stackdriver.Enabled, "USE_STACKDRIVER")
	if c.MetricsConfig.Stackdriver.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Stackdriver.Prefix, "STACKDRIVER_PREFIX")
		maybeSetFromEnv(&c.MetricsConfig.Stackdriver.ProjectID, "STACKDRIVER_PROJECT_ID")
	}

	maybeSetFromEnvBool(&c.MetricsConfig.Prometheus.Enabled, "USE_PROMETHEUS")
	if c.MetricsConfig.Prometheus.Enabled {
		maybeSetFromEnv(&c.MetricsConfig.Prometheus.Prefix, "PROMETHEUS_PREFIX")
		maybeSetFromEnvInt(&c.MetricsConfig.Prometheus.Port, "PROMETHEUS_PORT", &errs)
	}

	maybeSetFromEnv(&c.Proxy.Url, "PROXY_URL")
	maybeSetFromEnv(&c.Proxy.User, "PROXY_AUTH_USER")
	maybeSetFromEnv(&c.Proxy.Password, "PROXY_AUTH_PASSWORD")
	maybeSetFromEnv(&c.Proxy.Domain, "PROXY_AUTH_DOMAIN")
	maybeSetFromEnvBool(&c.Proxy.NtlmAuth, "PROXY_AUTH_NTLM")
	maybeSetFromEnv(&c.Proxy.CaCertFiles, "PROXY_CA_CERTS")

	if len(errs) > 0 {
		ss := make([]string, 0, len(errs))
		for _, e := range errs {
			ss = append(ss, e.Error())
		}
		return errors.New(strings.Join(ss, ", "))
	}

	return ValidateConfig(c)
}

func getLogLevelByName(levelName string) (ldlog.LogLevel, bool) {
	if levelName == "" {
		return ldlog.Info, true
	}
	for _, level := range []ldlog.LogLevel{ldlog.Debug, ldlog.Info, ldlog.Warn, ldlog.Error, ldlog.None} {
		if strings.EqualFold(level.Name(), levelName) {
			return level, true
		}
	}
	return 0, false
}

func maybeEnvStrPtr(name string) *string {
	if s := os.Getenv(name); s != "" {
		return &s
	}
	return nil
}

func maybeSetFromEnv(prop *string, name string) bool {
	if s := os.Getenv(name); s != "" {
		*prop = s
		return true
	}
	return false
}

func maybeSetFromEnvInt(prop *int, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		n := 0
		var err error
		if n, err = strconv.Atoi(s); err != nil {
			*errs = append(*errs, fmt.Errorf("%s must be an integer", name))
		} else {
			*prop = n
		}
		return true
	}
	return false
}

func maybeSetFromEnvInt32(prop *int32, name string, errs *[]error) bool {
	if s := os.Getenv(name); s != "" {
		var n int64
		var err error
		if n, err = strconv.ParseInt(s, 10, 32); err != nil {
			*errs = append(*errs, fmt.Errorf("%s must be an integer", name))
		} else {
			*prop = int32(n)
		}
		return true
	}
	return false
}

func maybeSetFromEnvBool(prop *bool, name string) bool {
	if s, found := os.LookupEnv(name); found {
		if s == "1" || s == "true" {
			*prop = true
		} else {
			*prop = false
		}
		return true
	}
	return false
}

func getEnvVarsWithPrefix(prefix string) ([]string, map[string]string) {
	names := []string{}
	values := make(map[string]string)
	allVars := os.Environ()
	sort.Strings(allVars)
	for _, e := range allVars {
		if strings.HasPrefix(e, prefix) {
			fields := strings.Split(e, "=")
			if len(fields) == 2 {
				strippedName := strings.TrimPrefix(fields[0], prefix)
				names = append(names, strippedName)
				values[strippedName] = fields[1]
			}
		}
	}
	return names, values
}

func strPtrToString(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}
