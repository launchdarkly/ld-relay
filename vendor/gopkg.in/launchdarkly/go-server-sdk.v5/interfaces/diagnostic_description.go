package interfaces

import "gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

// DiagnosticDescription is an optional interface for components to describe their own configuration.
//
// The SDK uses a simplified JSON representation of its configuration when recording diagnostics data.
// Any component type that implements DataStoreFactory, DataSourceFactory, etc. may choose to contribute
// values to this representation, although the SDK may or may not use them.
type DiagnosticDescription interface {
	// DescribeConfiguration should return a JSON value or ldvalue.Null().
	//
	// For custom components, this must be a string value that describes the basic nature of this component
	// implementation (e.g. "Redis"). Built-in LaunchDarkly components may instead return a JSON object
	// containing multiple properties specific to the LaunchDarkly diagnostic schema.
	DescribeConfiguration() ldvalue.Value
}
