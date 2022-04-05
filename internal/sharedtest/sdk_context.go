package sharedtest

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
)

type SDKContextImpl struct {
	sdkKey string
}

func (s SDKContextImpl) GetBasic() interfaces.BasicConfiguration {
	return interfaces.BasicConfiguration{SDKKey: s.sdkKey}
}

func (s SDKContextImpl) GetHTTP() interfaces.HTTPConfiguration {
	c, _ := ldcomponents.HTTPConfiguration().CreateHTTPConfiguration(s.GetBasic())
	return c
}

func (s SDKContextImpl) GetLogging() interfaces.LoggingConfiguration {
	c, _ := ldcomponents.Logging().Loggers(ldlog.NewDisabledLoggers()).CreateLoggingConfiguration(s.GetBasic())
	return c
}
