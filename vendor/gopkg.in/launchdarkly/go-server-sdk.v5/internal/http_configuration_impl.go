package internal

import (
	"net/http"
)

// HTTPConfigurationImpl is the internal implementation of HTTPConfiguration.
type HTTPConfigurationImpl struct {
	DefaultHeaders    http.Header
	HTTPClientFactory func() *http.Client
}

func (c HTTPConfigurationImpl) GetDefaultHeaders() http.Header { //nolint:golint // no doc comment for standard method
	// maps are mutable, so return a copy
	ret := make(http.Header, len(c.DefaultHeaders))
	for k, v := range c.DefaultHeaders {
		ret[k] = v
	}
	return ret
}

func (c HTTPConfigurationImpl) CreateHTTPClient() *http.Client { //nolint:golint // no doc comment for standard method
	if c.HTTPClientFactory == nil { // should not happen except possibly in tests
		client := *http.DefaultClient
		return &client
	}
	return c.HTTPClientFactory()
}
