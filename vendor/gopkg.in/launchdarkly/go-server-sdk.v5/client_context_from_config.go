package ldclient

import (
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
)

func newClientContextFromConfig(
	sdkKey string,
	config Config,
	diagnosticsManager *ldevents.DiagnosticsManager,
) (interfaces.ClientContext, error) {
	basicConfig := interfaces.BasicConfiguration{SDKKey: sdkKey, Offline: config.Offline}

	httpFactory := config.HTTP
	if httpFactory == nil {
		httpFactory = ldcomponents.HTTPConfiguration()
	}
	http, err := httpFactory.CreateHTTPConfiguration(basicConfig)
	if err != nil {
		return nil, err
	}

	loggingFactory := config.Logging
	if loggingFactory == nil {
		loggingFactory = ldcomponents.Logging()
	}
	logging, err := loggingFactory.CreateLoggingConfiguration(basicConfig)
	if err != nil {
		return nil, err
	}

	return internal.NewClientContextImpl(
		sdkKey,
		http,
		logging,
		config.Offline,
		diagnosticsManager,
	), nil
}
