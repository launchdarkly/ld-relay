package sharedtest

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
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
