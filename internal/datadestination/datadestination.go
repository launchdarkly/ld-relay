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
	logging         subsystems.LoggingConfiguration
	dataDestination subsystems.DataDestination
	readOnly        subsystems.ReadOnlyStore
	updates         streams.EnvStreamUpdates
	mu              sync.RWMutex
}

func (d *DataDestinationWrapper) GetReadOnlyStore() subsystems.ReadOnlyStore {
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
func (d *DataDestinationWrapper) SetDataSystemPieces(dd subsystems.DataDestination, ro subsystems.ReadOnlyStore) {
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
	allData, err := subsystems.ToStorableItems(events)
	if err != nil {
		d.logging.Loggers.Debugf("Failed to convert events to storable items", "error", err)
		return
	}

	d.dataDestination.SetBasis(events, selector, persist)

	if d.updates != nil {
		d.updates.SendAllDataUpdate(allData)
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
	allData, err := subsystems.ToStorableItems(events)
	if err != nil {
		d.logging.Loggers.Debugf("Failed to convert events to storable items", "error", err)
		return
	}

	d.dataDestination.ApplyDelta(events, selector, persist)
	if d.updates != nil {
		for _, collection := range allData {
			for _, item := range collection.Items {
				d.updates.SendSingleItemUpdate(collection.Kind, item.Key, item.Item)
			}
		}
	}
}
