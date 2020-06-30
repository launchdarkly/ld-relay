package interfaces

import (
	"net/http"
)

// HTTPConfiguration encapsulates top-level HTTP configuration that applies to all SDK components.
//
// See ldcomponents.HTTPConfigurationBuilder for more details on these properties.
type HTTPConfiguration interface {
	// GetDefaultHeaders returns the basic headers that should be added to all HTTP requests from
	// SDK components to LaunchDarkly services, based on the current SDK configuration.
	GetDefaultHeaders() http.Header

	// CreateHTTPClient returns a new HTTP client instance based on the SDK configuration.
	CreateHTTPClient() *http.Client
}

// HTTPConfigurationFactory is an interface for a factory that creates an HTTPConfiguration.
type HTTPConfigurationFactory interface {
	// CreateHTTPConfiguration is called internally by the SDK to obtain the configuration.
	//
	// This happens only when MakeClient or MakeCustomClient is called. If the factory returns
	// an error, creation of the LDClient fails.
	CreateHTTPConfiguration(basicConfig BasicConfiguration) (HTTPConfiguration, error)
}
