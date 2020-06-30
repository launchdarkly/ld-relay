package ldcomponents

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
)

type nullDataSourceFactory struct{}

// ExternalUpdatesOnly returns a configuration object that disables a direct connection with LaunchDarkly
// for feature flag updates.
//
// Storing this in LDConfig.DataSource causes the SDK not to retrieve feature flag data from LaunchDarkly,
// regardless of any other configuration. This is normally done if you are using the Relay Proxy
// (https://docs.launchdarkly.com/docs/the-relay-proxy) in "daemon mode", where an external process-- the
// Relay Proxy-- connects to LaunchDarkly and populates a persistent data store with the feature flag data.
// The data store could also be populated by another process that is running the LaunchDarkly SDK. If there
// is no external process updating the data store, then the SDK will not have any feature flag data and
// will return application default values only.
//
//     config := ld.Config{
//         DataSource: ldcomponents.ExternalUpdatesOnly(),
//     }
func ExternalUpdatesOnly() interfaces.DataSourceFactory {
	return nullDataSourceFactory{}
}

// DataSourceFactory implementation
func (f nullDataSourceFactory) CreateDataSource(
	context interfaces.ClientContext,
	dataSourceUpdates interfaces.DataSourceUpdates,
) (interfaces.DataSource, error) {
	context.GetLogging().GetLoggers().Info("LaunchDarkly client will not connect to Launchdarkly for feature flag data")
	if dataSourceUpdates != nil {
		dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
	}
	return internal.NewNullDataSource(), nil
}

// DiagnosticDescription implementation
func (f nullDataSourceFactory) DescribeConfiguration() ldvalue.Value {
	// This information is only used for diagnostic events, and if we're able to send diagnostic events,
	// then by definition we're not completely offline so we must be using daemon mode.
	return ldvalue.ObjectBuild().
		Set("streamingDisabled", ldvalue.Bool(false)).
		Set("customBaseURI", ldvalue.Bool(false)).
		Set("customStreamURI", ldvalue.Bool(false)).
		Set("usingRelayDaemon", ldvalue.Bool(true)).
		Build()
}
