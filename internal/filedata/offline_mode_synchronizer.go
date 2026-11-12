package filedata

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// simpleBroadcaster is a simple implementation of a broadcaster for changeSets and status updates.
//
// Each listener has a one-slot mailbox. Every update made visible to a listener replaces any
// still-unread one, so a listener that cannot keep up loses only intermediate updates, never the
// newest one. This is safe only for values that each describe the whole current state. Every
// changeSet broadcast here is a full transfer that replaces the SDK's data set, so after any burst
// the listener reads the newest data, which is what correctness depends on here.
type simpleBroadcaster[T any] struct {
	mu        sync.RWMutex
	listeners []chan T
}

func newSimpleBroadcaster[T any]() *simpleBroadcaster[T] {
	return &simpleBroadcaster[T]{
		listeners: make([]chan T, 0),
	}
}

func (b *simpleBroadcaster[T]) AddListener() <-chan T {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan T, 1)
	b.listeners = append(b.listeners, ch)
	return ch
}

func (b *simpleBroadcaster[T]) RemoveListener(ch <-chan T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, listener := range b.listeners {
		if listener == ch {
			// Remove by swapping with last element and truncating
			b.listeners[i] = b.listeners[len(b.listeners)-1]
			b.listeners = b.listeners[:len(b.listeners)-1]
			close(listener)
			break
		}
	}
}

// Broadcast makes value visible to every listener. It never blocks on a slow listener: a listener
// whose mailbox still holds an unread older update has that update replaced, so the listener's
// next receive yields the newest value. To keep the replace atomic across concurrent broadcasts
// it takes the write lock rather than a read lock; the broadcaster has few listeners and the
// broadcasts are rare, so the cost is nil in practice.
func (b *simpleBroadcaster[T]) Broadcast(value T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.listeners {
		select {
		case ch <- value:
		default:
			// The mailbox holds an unread older update. Replace it, so the listener's next receive
			// yields this newest value. Holding the write lock makes the pop-then-send atomic
			// against other Broadcast calls and against RemoveListener; a receiving consumer reads
			// by value and never mutates the mailbox contents, so it cannot race the swap.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- value:
			default:
				// Unreachable while the mailbox is one slot. Fall back to silently skipping the
				// value rather than blocking if that ever stops holding.
			}
		}
	}
}

// OfflineModeData holds the current archive data. It outlives the synchronizers that read it: the
// SDK can close a synchronizer and build a new one later, and each new synchronizer reads the
// current data from here.
type OfflineModeData struct {
	mu                   sync.RWMutex
	currentChangeSet     *subsystems.ChangeSet
	initError            error
	changeSetBroadcaster *simpleBroadcaster[subsystems.ChangeSet]
}

// NewOfflineModeData creates the data holder from the archive's initial data.
func NewOfflineModeData(initialData []ldstoretypes.Collection) *OfflineModeData {
	d := &OfflineModeData{
		changeSetBroadcaster: newSimpleBroadcaster[subsystems.ChangeSet](),
	}

	// Convert initial data to ChangeSet
	changeSet, err := makeChangeSetFromCollections(initialData)
	if err != nil {
		d.initError = err
	} else {
		d.currentChangeSet = changeSet
	}

	return d
}

func (d *OfflineModeData) current() (*subsystems.ChangeSet, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.currentChangeSet, d.initError
}

// OfflineModeSynchronizerFactory creates a synchronizer that loads data from an external source
// (the relay's archive file) without making any network connections.
type OfflineModeSynchronizerFactory struct {
	Data *OfflineModeData
}

// OfflineModeSynchronizer implements subsystems.DataSynchronizer for offline mode.
// It loads pre-populated data from the archive file without connecting to LaunchDarkly.
type OfflineModeSynchronizer struct {
	data   *OfflineModeData
	quit   chan struct{}
	closed atomic.Bool
}

// Build returns a new synchronizer each time. Close ends a synchronizer permanently, and the SDK
// can build the synchronizer again after it closes it.
func (f OfflineModeSynchronizerFactory) Build(
	ctx subsystems.ClientContext,
) (subsystems.DataSynchronizer, error) {
	return &OfflineModeSynchronizer{
		data: f.Data,
		quit: make(chan struct{}),
	}, nil
}

func (s *OfflineModeSynchronizer) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	close(s.quit)
	return nil
}

func (s *OfflineModeSynchronizer) Name() string {
	return "OfflineModeSynchronizer"
}

// Fetch returns the current basis (full dataset) from the offline data source.
func (s *OfflineModeSynchronizer) Fetch(ds subsystems.DataSelector, ctx context.Context) (*subsystems.Basis, bool, error) {
	changeSet, initError := s.data.current()

	if initError != nil {
		return nil, false, initError
	}
	if changeSet == nil {
		return nil, false, errors.New("no data available in offline mode")
	}

	return &subsystems.Basis{
		ChangeSet: *changeSet,
		Persist:   false,
	}, false, nil
}

// Sync starts the synchronizer and returns a channel for receiving updates.
// For offline mode, this immediately provides the data from the archive file.
func (s *OfflineModeSynchronizer) Sync(ds subsystems.DataSelector) <-chan subsystems.DataSynchronizerResult {
	resultChan := make(chan subsystems.DataSynchronizerResult)
	changeSetChan := s.data.changeSetBroadcaster.AddListener()

	go func() {
		defer close(resultChan)
		defer s.data.changeSetBroadcaster.RemoveListener(changeSetChan)

		// send gives up when the synchronizer closes, so that the goroutine does not block forever
		// if the SDK stops reading before it calls Close.
		send := func(result subsystems.DataSynchronizerResult) bool {
			select {
			case resultChan <- result:
				return true
			case <-s.quit:
				return false
			}
		}

		// Get initial data from stored state
		changeSet, initError := s.data.current()

		result := subsystems.DataSynchronizerResult{
			State: interfaces.DataSourceStateInitializing,
		}

		if initError != nil {
			result.State = interfaces.DataSourceStateOff
			result.Error = interfaces.DataSourceErrorInfo{
				Kind:    interfaces.DataSourceErrorKindUnknown,
				Message: initError.Error(),
			}
		} else {
			result.State = interfaces.DataSourceStateValid
			result.ChangeSet = changeSet
		}
		if !send(result) {
			return
		}

		// Listen for updates (in offline mode, these would be file changes)
		for {
			select {
			case <-s.quit:
				return
			case changeSet, ok := <-changeSetChan:
				if !ok {
					return
				}
				result.ChangeSet = &changeSet
				result.State = interfaces.DataSourceStateValid
				result.Error = interfaces.DataSourceErrorInfo{} // Clear any previous error
				if !send(result) {
					return
				}
			}
		}
	}()

	return resultChan
}

// makeChangeSetFromCollections converts old-style Collection data to a new-style ChangeSet.
// This uses the SDK's NewChangeSetFromCollections which pre-caches the collections,
// avoiding redundant conversions when the data is accessed later.
//
// The changeSet is a full transfer with no selector.
func makeChangeSetFromCollections(collections []ldstoretypes.Collection) (*subsystems.ChangeSet, error) {
	return subsystems.NewChangeSetFromCollections(
		subsystems.ServerIntent{Payload: subsystems.Payload{Code: subsystems.IntentTransferFull}},
		subsystems.NoSelector(),
		collections,
	)
}

// UpdateData allows external updates to the data (e.g., when the archive file changes).
func (d *OfflineModeData) UpdateData(collections []ldstoretypes.Collection) error {
	changeSet, err := makeChangeSetFromCollections(collections)
	if err != nil {
		return err
	}

	// Hold the lock through the broadcast. Otherwise two concurrent calls can broadcast in the
	// opposite order to the one in which they stored their changeSets, and listeners end on data
	// that is no longer current. Broadcast never blocks, so holding the lock is safe.
	d.mu.Lock()
	defer d.mu.Unlock()
	d.currentChangeSet = changeSet
	d.initError = nil // Clear any previous initialization error

	d.changeSetBroadcaster.Broadcast(*changeSet)
	return nil
}
