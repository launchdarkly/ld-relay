package internal

import (
	"time"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// dataSourceStatusProviderImpl is the internal implementation of DataSourceStatusProvider. It's not
// exported because the rest of the SDK code only interacts with the public interface.
type dataSourceStatusProviderImpl struct {
	broadcaster       *DataSourceStatusBroadcaster
	dataSourceUpdates *DataSourceUpdatesImpl
}

// NewDataSourceStatusProviderImpl creates the internal implementation of DataSourceStatusProvider.
func NewDataSourceStatusProviderImpl(
	broadcaster *DataSourceStatusBroadcaster,
	dataSourceUpdates *DataSourceUpdatesImpl,
) interfaces.DataSourceStatusProvider {
	return &dataSourceStatusProviderImpl{broadcaster, dataSourceUpdates}
}

func (d *dataSourceStatusProviderImpl) GetStatus() interfaces.DataSourceStatus {
	return d.dataSourceUpdates.GetLastStatus()
}

func (d *dataSourceStatusProviderImpl) AddStatusListener() <-chan interfaces.DataSourceStatus {
	return d.broadcaster.AddListener()
}

func (d *dataSourceStatusProviderImpl) RemoveStatusListener(listener <-chan interfaces.DataSourceStatus) {
	d.broadcaster.RemoveListener(listener)
}

func (d *dataSourceStatusProviderImpl) WaitFor(desiredState interfaces.DataSourceState, timeout time.Duration) bool {
	return d.dataSourceUpdates.waitFor(desiredState, timeout)
}
