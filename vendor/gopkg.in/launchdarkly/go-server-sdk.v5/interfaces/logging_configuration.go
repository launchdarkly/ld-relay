package interfaces

import (
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// LoggingConfiguration encapsulates the SDK's general logging configuration.
//
// See ldcomponents.LoggingConfigurationBuilder for more details on these properties.
type LoggingConfiguration interface {
	// GetLoggers returns the configured ldlog.Loggers instance.
	GetLoggers() ldlog.Loggers

	// GetLogDataSourceOutageAsErrorAfter returns the time threshold, if any, after which the SDK
	// will log a data source outage at Error level instead of Warn level. See
	// LoggingConfigurationBuilderLogDataSourceOutageAsErrorAfter().
	GetLogDataSourceOutageAsErrorAfter() time.Duration

	// IsLogEvaluationErrors returns true if evaluation errors should be logged.
	IsLogEvaluationErrors() bool

	// IsLogUserKeyInErrors returns true if user keys may be included in logging.
	IsLogUserKeyInErrors() bool
}

// LoggingConfigurationFactory is an interface for a factory that creates a LoggingConfiguration.
type LoggingConfigurationFactory interface {
	// CreateLoggingConfiguration is called internally by the SDK to obtain the configuration.
	//
	// This happens only when MakeClient or MakeCustomClient is called. If the factory returns
	// an error, creation of the LDClient fails.
	CreateLoggingConfiguration(basicConfig BasicConfiguration) (LoggingConfiguration, error)
}
