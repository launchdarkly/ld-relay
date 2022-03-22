package sdks

import (
	ld "github.com/launchdarkly/go-server-sdk/v6"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
)

// NewSimpleClientContext creates a simple implementation of the SDK's ClientContext interface for
// initializing SDK components. The SDK doesn't surface a way to do this because components aren't
// normally created outside of its own constructor.
func NewSimpleClientContext(sdkKey string, sdkConfig ld.Config) interfaces.ClientContext {
	basic := interfaces.BasicConfiguration{SDKKey: sdkKey}
	if sdkConfig.HTTP == nil {
		sdkConfig.HTTP = ldcomponents.HTTPConfiguration()
	}
	http, _ := sdkConfig.HTTP.CreateHTTPConfiguration(basic)
	if sdkConfig.Logging == nil {
		sdkConfig.Logging = ldcomponents.Logging()
	}
	logging, _ := sdkConfig.Logging.CreateLoggingConfiguration(basic)
	return simpleClientContext{basic: basic, http: http, logging: logging}
}

type simpleClientContext struct {
	basic   interfaces.BasicConfiguration
	http    interfaces.HTTPConfiguration
	logging interfaces.LoggingConfiguration
}

func (s simpleClientContext) GetBasic() interfaces.BasicConfiguration     { return s.basic }
func (s simpleClientContext) GetHTTP() interfaces.HTTPConfiguration       { return s.http }
func (s simpleClientContext) GetLogging() interfaces.LoggingConfiguration { return s.logging }
