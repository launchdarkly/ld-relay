// Package ldcomponents provides the standard implementations and configuration options of LaunchDarkly components.
//
// Some configuration options are represented as fields in the main Config struct; but others are specific to one
// area of functionality, such as how the SDK receives feature flag updates or processes analytics events. For the
// latter, the standard way to specify a configuration is to call one of the functions in ldcomponents (such as
// StreamingDataSource), apply any desired configuration change to the object that that method returns (such as
// StreamingDataSourceBuilder.InitialReconnectDelay), and then put the configured component builder into the
// corresponding Config field (such as Config.DataSource) to use that configuration in the SDK.
package ldcomponents
