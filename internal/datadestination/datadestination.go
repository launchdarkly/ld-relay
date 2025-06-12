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
	readOnly subsystems.ReadOnlyDataStore
	updates  streams.EnvStreamUpdates
	mu       sync.RWMutex
}

func (d *DataDestinationWrapper) GetReadOnlyStore() subsystems.ReadOnlyDataStore {
	d.mu.RLock()
	s := d.readOnly
	d.mu.RUnlock()

	return s
}

// SetDataSystemPieces sets the ReadOnlyStore and the changeSetUpdates channel
// for this wrapper. This allows the relay access to underlying store data, and
// to receive updates as the SDK's data system receives them.
func (d *DataDestinationWrapper) SetDataSystemPieces(ro subsystems.ReadOnlyDataStore, changeSetUpdates <-chan subsystems.ChangeSet) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// d.dataDestination = dd
	d.readOnly = ro

	canReceiveUpdates := d.updates != nil

	go func() {
		// This goroutine listens for change sets and applies them to the updates stream.
		// It will continue to run until the changeSetUpdates channel is closed.
		for changeSet := range changeSetUpdates {
			if !canReceiveUpdates {
				continue
			}

			switch changeSet.IntentCode() {
			case subsystems.IntentTransferFull:
				d.updates.SetBasis(changeSet.Changes(), changeSet.Selector())
			case subsystems.IntentTransferChanges:
				d.updates.ApplyDelta(changeSet.Changes(), changeSet.Selector())
			}
		}
	}()
}
