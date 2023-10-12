package sdks

import (
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

// NewSimpleClientContext creates a simple implementation of the SDK's ClientContext interface for
// initializing SDK components. The SDK doesn't surface a way to do this because components aren't
// normally created outside of its own constructor.
func NewSimpleClientContext(sdkKey string, sdkConfig ld.Config) subsystems.ClientContext {
	ret := subsystems.BasicClientContext{SDKKey: sdkKey}
	if sdkConfig.HTTP == nil {
		sdkConfig.HTTP = ldcomponents.HTTPConfiguration()
	}
	ret.HTTP, _ = sdkConfig.HTTP.Build(ret)
	if sdkConfig.Logging == nil {
		sdkConfig.Logging = ldcomponents.Logging()
	}
	ret.Logging, _ = sdkConfig.Logging.Build(ret)
	return ret
}
