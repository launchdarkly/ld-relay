package ldcomponents

import (
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
)

// LoggingConfigurationBuilder contains methods for configuring the SDK's logging behavior.
//
// If you want to set non-default values for any of these properties, create a builder with
// ldcomponents.Logging(), change its properties with the LoggingConfigurationBuilder methods, and
// store it in Config.Logging:
//
//     config := ld.Config{
//         Logging: ldcomponents.Logging().MinLevel(ldlog.Warn),
//     }
type LoggingConfigurationBuilder struct {
	config internal.LoggingConfigurationImpl
}

// DefaultLogDataSourceOutageAsErrorAfter is the default value for
// LoggingConfigurationBuilder.LogDataSourceOutageAsErrorAfter(): one minute.
const DefaultLogDataSourceOutageAsErrorAfter = time.Minute

// Logging returns a configuration builder for the SDK's logging configuration.
//
// The default configuration has logging enabled with default settings. If you want to set non-default
// values for any of these properties, create a builder with ldcomponents.Logging(), change its properties
// with the LoggingConfigurationBuilder methods, and store it in Config.Logging:
//
//     config := ld.Config{
//         Logging: ldcomponents.Logging().MinLevel(ldlog.Warn),
//     }
func Logging() *LoggingConfigurationBuilder {
	return &LoggingConfigurationBuilder{
		config: internal.LoggingConfigurationImpl{
			LogDataSourceOutageAsErrorAfter: DefaultLogDataSourceOutageAsErrorAfter,
			Loggers:                         ldlog.NewDefaultLoggers(),
		},
	}
}

// LogDataSourceOutageAsErrorAfter sets the time threshold, if any, after which the SDK will log a data
// source outage at Error level instead of Warn level.
//
// A data source outage means that an error condition, such as a network interruption or an error from
// the LaunchDarkly service, is preventing the SDK from receiving feature flag updates. Many outages are
// brief and the SDK can recover from them quickly; in that case it may be undesirable to log an
// Error line, which might trigger an unwanted automated alert depending on your monitoring
// tools. So, by default, the SDK logs such errors at Warn level. However, if the amount of time
// specified by this method elapses before the data source starts working again, the SDK will log an
// additional message at Error level to indicate that this is a sustained problem.
//
// The default is DefaultLogDataSourceOutageAsErrorAfter (one minute). Setting it to zero will disable
// this feature, so you will only get Warn messages.
func (b *LoggingConfigurationBuilder) LogDataSourceOutageAsErrorAfter(
	logDataSourceOutageAsErrorAfter time.Duration,
) *LoggingConfigurationBuilder {
	b.config.LogDataSourceOutageAsErrorAfter = logDataSourceOutageAsErrorAfter
	return b
}

// LogEvaluationErrors sets whether the client should log a warning message whenever a flag cannot be evaluated due
// to an error (e.g. there is no flag with that key, or the user properties are invalid). By default, these messages
// are not logged, although you can detect such errors programmatically using the VariationDetail methods.
func (b *LoggingConfigurationBuilder) LogEvaluationErrors(logEvaluationErrors bool) *LoggingConfigurationBuilder {
	b.config.LogEvaluationErrors = logEvaluationErrors
	return b
}

// LogUserKeyInErrors sets whether log messages for errors related to a specific user can include the user key. By
// default, they will not, since the user key might be considered privileged information.
func (b *LoggingConfigurationBuilder) LogUserKeyInErrors(logUserKeyInErrors bool) *LoggingConfigurationBuilder {
	b.config.LogUserKeyInErrors = logUserKeyInErrors
	return b
}

// Loggers specifies an instance of ldlog.Loggers to use for SDK logging. The ldlog package contains
// methods for customizing the destination and level filtering of log output.
func (b *LoggingConfigurationBuilder) Loggers(loggers ldlog.Loggers) *LoggingConfigurationBuilder {
	b.config.Loggers = loggers
	return b
}

// MinLevel specifies the minimum level for log output, where ldlog.Debug is the lowest and ldlog.Error
// is the highest. Log messages at a level lower than this will be suppressed. The default is
// ldlog.Info.
//
// This is equivalent to creating an ldlog.Loggers instance, calling SetMinLevel() on it, and then
// passing it to LoggingConfigurationBuilder.Loggers().
func (b *LoggingConfigurationBuilder) MinLevel(level ldlog.LogLevel) *LoggingConfigurationBuilder {
	b.config.Loggers.SetMinLevel(level)
	return b
}

// CreateLoggingConfiguration is called internally by the SDK.
func (b *LoggingConfigurationBuilder) CreateLoggingConfiguration(
	basic interfaces.BasicConfiguration,
) (interfaces.LoggingConfiguration, error) {
	return b.config, nil
}

// NoLogging returns a configuration object that disables logging.
//
//     config := ld.Config{
//         Logging: ldcomponents.NoLogging(),
//     }
func NoLogging() interfaces.LoggingConfigurationFactory {
	return noLoggingConfigurationFactory{}
}

type noLoggingConfigurationFactory struct{}

func (f noLoggingConfigurationFactory) CreateLoggingConfiguration(
	basic interfaces.BasicConfiguration,
) (interfaces.LoggingConfiguration, error) {
	return internal.LoggingConfigurationImpl{Loggers: ldlog.NewDisabledLoggers()}, nil
}
