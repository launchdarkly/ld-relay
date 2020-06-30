package sharedtest

import (
	"net/http"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

type stubClientContext struct {
	sdkKey  string
	http    interfaces.HTTPConfiguration
	logging interfaces.LoggingConfiguration
}

// NewSimpleTestContext returns a basic implementation of interfaces.ClientContext for use in test code.
func NewSimpleTestContext(sdkKey string) interfaces.ClientContext {
	return NewTestContext(sdkKey, TestHTTPConfig(), TestLoggingConfig())
}

// NewTestContext returns a basic implementation of interfaces.ClientContext for use in test code.
func NewTestContext(
	sdkKey string,
	http interfaces.HTTPConfiguration,
	logging interfaces.LoggingConfiguration,
) interfaces.ClientContext {
	return stubClientContext{sdkKey, http, logging}
}

func (c stubClientContext) GetBasic() interfaces.BasicConfiguration {
	return interfaces.BasicConfiguration{SDKKey: c.sdkKey}
}

func (c stubClientContext) GetHTTP() interfaces.HTTPConfiguration {
	return c.http
}

func (c stubClientContext) GetLogging() interfaces.LoggingConfiguration {
	return c.logging
}

// TestHTTP returns a basic HTTPConfigurationFactory for test code.
func TestHTTP() interfaces.HTTPConfigurationFactory {
	return testHTTPConfigurationFactory{}
}

// TestHTTPConfig returns a basic HTTPConfiguration for test code.
func TestHTTPConfig() interfaces.HTTPConfiguration {
	return testHTTPConfiguration{}
}

// TestLogging returns a LoggingConfigurationFactory corresponding to NewTestLoggers().
func TestLogging() interfaces.LoggingConfigurationFactory {
	return testLoggingConfigurationFactory{}
}

// TestLoggingConfig returns a LoggingConfiguration corresponding to NewTestLoggers().
func TestLoggingConfig() interfaces.LoggingConfiguration {
	return testLoggingConfiguration{}
}

type testHTTPConfiguration struct{}
type testHTTPConfigurationFactory struct{}

func (c testHTTPConfiguration) GetDefaultHeaders() http.Header {
	return nil
}

func (c testHTTPConfiguration) CreateHTTPClient() *http.Client {
	client := *http.DefaultClient
	return &client
}

func (c testHTTPConfigurationFactory) CreateHTTPConfiguration(
	basicConfig interfaces.BasicConfiguration,
) (interfaces.HTTPConfiguration, error) {
	return testHTTPConfiguration{}, nil
}

type testLoggingConfiguration struct{}
type testLoggingConfigurationFactory struct{}

func (c testLoggingConfiguration) IsLogEvaluationErrors() bool {
	return false
}

func (c testLoggingConfiguration) IsLogUserKeyInErrors() bool {
	return false
}

func (c testLoggingConfiguration) GetLogDataSourceOutageAsErrorAfter() time.Duration {
	return 0
}

func (c testLoggingConfiguration) GetLoggers() ldlog.Loggers {
	return NewTestLoggers()
}

func (c testLoggingConfigurationFactory) CreateLoggingConfiguration(
	basicConfig interfaces.BasicConfiguration,
) (interfaces.LoggingConfiguration, error) {
	return testLoggingConfiguration{}, nil
}

type contextWithDiagnostics struct {
	sdkKey             string
	headers            http.Header
	httpClientFactory  func() *http.Client
	diagnosticsManager *ldevents.DiagnosticsManager
}

func (c *contextWithDiagnostics) GetBasic() interfaces.BasicConfiguration {
	return interfaces.BasicConfiguration{SDKKey: c.sdkKey}
}

func (c *contextWithDiagnostics) GetHTTP() interfaces.HTTPConfiguration {
	return TestHTTPConfig()
}

func (c *contextWithDiagnostics) GetLogging() interfaces.LoggingConfiguration {
	return TestLoggingConfig()
}

func (c *contextWithDiagnostics) CreateHTTPClient() *http.Client {
	if c.httpClientFactory == nil {
		return http.DefaultClient
	}
	return c.httpClientFactory()
}

func (c *contextWithDiagnostics) GetDiagnosticsManager() *ldevents.DiagnosticsManager {
	return c.diagnosticsManager
}

// NewClientContextWithDiagnostics returns a ClientContext implementation for testing that includes
// a DiagnosticsManager.
func NewClientContextWithDiagnostics(
	sdkKey string,
	headers http.Header,
	httpClientFactory func() *http.Client,
	diagnosticsManager *ldevents.DiagnosticsManager,
) interfaces.ClientContext {
	return &contextWithDiagnostics{sdkKey, headers, httpClientFactory, diagnosticsManager}
}
