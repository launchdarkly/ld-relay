package datadestination

import (
	"sync"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"
)

func NewDataDesinationWrapper(updates streams.EnvStreamUpdates) *DataDestinationWrapper {
	return &DataDestinationWrapper{
		updates: updates,
		mu:      sync.RWMutex{},
	}
}

type DataDestinationWrapper struct {
	dataDestination subsystems.DataDestination
	readOnly        subsystems.ReadOnlyDataStore
	updates         streams.EnvStreamUpdates
	mu              sync.RWMutex
}

func (d *DataDestinationWrapper) GetReadOnlyStore() subsystems.ReadOnlyDataStore {
	d.mu.RLock()
	s := d.readOnly
	d.mu.RUnlock()

	return s
}

// GetUpdates returns the EnvStreamUpdates that will receive all updates sent to this store. This is
// exposed for testing so that we can simulate receiving updates from LaunchDarkly to this component.
func (d *DataDestinationWrapper) GetUpdates() streams.EnvStreamUpdates {
	d.mu.RLock()
	updates := d.updates
	d.mu.RUnlock()
	return updates
}

// SetDataSystemPieces sets the DataDestination and ReadOnlyStore for this
// wrapper. This allows the relay access to both persist, and query, for
// information managed by the SDKs' data system.
func (d *DataDestinationWrapper) SetDataSystemPieces(dd subsystems.DataDestination, ro subsystems.ReadOnlyDataStore) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dataDestination = dd
	d.readOnly = ro
}

// Selector returns the last known selector for the data store. If no previous selector is known,
// this should return subsystems.NoSelector()
func (d *DataDestinationWrapper) Selector() subsystems.Selector {
	return d.dataDestination.Selector()
}

// SetBasis defines a new basis for the data store. This means the store must
// be emptied of any existing data before applying the events. This operation should be
// atomic with respect to any other operations that modify the store.
//
// The selector defines the version of the basis.
//
// If persist is true, it indicates that the data should be propagated to any connected persistent
// store.
func (d *DataDestinationWrapper) SetBasis(events []subsystems.Change, selector subsystems.Selector, persist bool) {
	d.dataDestination.SetBasis(events, selector, persist)

	if d.updates != nil {
		d.updates.SetBasis(events, selector)
	}
}

// ApplyDelta applies a set of changes to an existing basis. This operation should be atomic with
// respect to any other operations that modify the store.
//
// The selector defines the new version of the basis.
//
// If persist is true, it indicates that the changes should be propagated to any connected persistent
// store.
func (d *DataDestinationWrapper) ApplyDelta(events []subsystems.Change, selector subsystems.Selector, persist bool) {
	d.dataDestination.ApplyDelta(events, selector, persist)
	if d.updates != nil {
		d.updates.ApplyDelta(events, selector)
	}
}
