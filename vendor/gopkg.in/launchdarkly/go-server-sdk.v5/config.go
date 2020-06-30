package ldclient

import (
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// Config exposes advanced configuration options for the LaunchDarkly client.
//
// All of these settings are optional, so an empty Config struct is always valid. See the description of each
// field for the default behavior if it is not set.
type Config struct {
	// Sets the implementation of DataSource for receiving feature flag updates.
	//
	// If nil, the default is ldcomponents.StreamingDataSource(); see that method for an explanation of how to
	// further configure streaming behavior. Other options include ldcomponents.PollingDataSource(),
	// ldcomponents.ExternalUpdatesOnly(), ldfiledata.DataSource(), or a custom implementation for testing.
	//
	// If Offline is set to true, then DataSource is ignored.
	//
	//     // using streaming mode and setting streaming options
	//     config.DataSource = ldcomponents.StreamingDataSource().InitialReconnectDelay(time.Second)
	//
	//     // using polling mode and setting polling options
	//     config.DataSource = ldcomponents.PollingDataSource().PollInterval(time.Minute)
	//
	//     // specifying that data will be updated by an external process (such as the Relay Proxy)
	//     config.DataSource = ldcomponents.ExternalUpdatesOnly()
	DataSource interfaces.DataSourceFactory

	// Sets the implementation of DataStore for holding feature flags and related data received from
	// LaunchDarkly.
	//
	// If nil, the default is ldcomponents.InMemoryDataStore(). Other available implementations include the
	// database integrations in the ldredis, ldconsul, and lddynamodb packages.
	DataStore interfaces.DataStoreFactory

	// Set to true to opt out of sending diagnostic events.
	//
	// Unless DiagnosticOptOut is set to true, the client will send some diagnostics data to the LaunchDarkly
	// servers in order to assist in the development of future SDK improvements. These diagnostics consist of an
	// initial payload containing some details of the SDK in use, the SDK's configuration, and the platform the
	// SDK is being run on, as well as payloads sent periodically with information on irregular occurrences such
	// as dropped events.
	DiagnosticOptOut bool

	// Sets the SDK's behavior regarding analytics events.
	//
	// If nil, the default is ldcomponents.SendEvents(); see that method for an explanation of how to further
	// configure event delivery. You may also turn off event delivery using ldcomponents.NoEvents().
	//
	// If Offline is set to true, then event delivery is always off and Events is ignored.
	Events interfaces.EventProcessorFactory

	// Provides configuration of the SDK's network connection behavior.
	//
	// If nil, the default is ldcomponents.HTTPConfiguration(); see that method for an explanation of how to
	// further configure these options.
	//
	// If Offline is set to true, then HTTP is ignored.
	HTTP interfaces.HTTPConfigurationFactory

	// Provides configuration of the SDK's logging behavior.
	//
	// If nil, the default is ldcomponents.Logging(); see that method for an explanation of how to
	// further configure logging behavior. The other option is ldcomponents.NoLogging().
	Logging interfaces.LoggingConfigurationFactory

	// Sets whether this client is offline. An offline client will not make any network connections to LaunchDarkly,
	// and will return default values for all feature flags.
	Offline bool
}
