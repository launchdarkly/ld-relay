package internal

import (
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// clientContextImpl is the SDK's standard implementation of interfaces.ClientContext.
type clientContextImpl struct {
	basic   interfaces.BasicConfiguration
	http    interfaces.HTTPConfiguration
	logging interfaces.LoggingConfiguration
	// Used internally to share a diagnosticsManager instance between components.
	diagnosticsManager *ldevents.DiagnosticsManager
}

// HasDiagnosticsManager is an interface that is implemented only by the SDK's own ClientContext
// implementation, to allow component factories to access the DiagnosticsManager.
type HasDiagnosticsManager interface {
	GetDiagnosticsManager() *ldevents.DiagnosticsManager
}

// NewClientContextImpl creates the SDK's standard implementation of interfaces.ClientContext.
func NewClientContextImpl(
	sdkKey string,
	http interfaces.HTTPConfiguration,
	logging interfaces.LoggingConfiguration,
	offline bool,
	diagnosticsManager *ldevents.DiagnosticsManager,
) interfaces.ClientContext {
	return &clientContextImpl{
		interfaces.BasicConfiguration{SDKKey: sdkKey, Offline: offline},
		http,
		logging,
		diagnosticsManager,
	}
}

func (c *clientContextImpl) GetBasic() interfaces.BasicConfiguration {
	return c.basic
}

func (c *clientContextImpl) GetHTTP() interfaces.HTTPConfiguration {
	return c.http
}

func (c *clientContextImpl) GetLogging() interfaces.LoggingConfiguration {
	return c.logging
}

// This method is accessed by components like StreamProcessor by checking for a private interface.
func (c *clientContextImpl) GetDiagnosticsManager() *ldevents.DiagnosticsManager {
	return c.diagnosticsManager
}
