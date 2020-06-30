package internal

import (
	"sync"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// DataStoreUpdatesImpl is the internal implementation of DataStoreUpdates. It is exported
// because the actual implementation type, rather than the interface, is required as a dependency
// of other SDK components.
type DataStoreUpdatesImpl struct {
	lastStatus  interfaces.DataStoreStatus
	broadcaster *DataStoreStatusBroadcaster
	lock        sync.Mutex
}

// NewDataStoreUpdatesImpl creates the internal implementation of DataStoreUpdates.
func NewDataStoreUpdatesImpl(broadcaster *DataStoreStatusBroadcaster) *DataStoreUpdatesImpl {
	return &DataStoreUpdatesImpl{
		lastStatus:  interfaces.DataStoreStatus{Available: true},
		broadcaster: broadcaster,
	}
}

func (d *DataStoreUpdatesImpl) getStatus() interfaces.DataStoreStatus {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.lastStatus
}

func (d *DataStoreUpdatesImpl) getBroadcaster() *DataStoreStatusBroadcaster {
	return d.broadcaster
}

// UpdateStatus is called from the data store to push a status update.
func (d *DataStoreUpdatesImpl) UpdateStatus(newStatus interfaces.DataStoreStatus) {
	d.lock.Lock()
	modified := false
	if newStatus != d.lastStatus {
		d.lastStatus = newStatus
		modified = true
	}
	d.lock.Unlock()
	if modified {
		d.broadcaster.Broadcast(newStatus)
	}
}
