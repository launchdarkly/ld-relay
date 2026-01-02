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
	ch := make(chan T, 10)
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

func (b *simpleBroadcaster[T]) Broadcast(value T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.listeners {
		select {
		case ch <- value:
		default:
			// If channel is full, skip this listener
		}
	}
}

// OfflineModeSynchronizerFactory creates a synchronizer that loads data from an external source
// (the relay's archive file) without making any network connections.
type OfflineModeSynchronizerFactory struct {
	Synchronizer *OfflineModeSynchronizer
}

// OfflineModeSynchronizer implements subsystems.DataSynchronizer for offline mode.
// It loads pre-populated data from the archive file without connecting to LaunchDarkly.
type OfflineModeSynchronizer struct {
	mu                   sync.RWMutex
	currentChangeSet     *subsystems.ChangeSet
	initError            error
	changeSetBroadcaster *simpleBroadcaster[subsystems.ChangeSet]
	statusBroadcaster    *simpleBroadcaster[interfaces.DataSynchronizerStatus]
	version              int32
	quit                 chan struct{}
	closed               atomic.Bool
}

func NewOfflineModeSynchronizer(initialData []ldstoretypes.Collection) *OfflineModeSynchronizer {
	s := &OfflineModeSynchronizer{
		changeSetBroadcaster: newSimpleBroadcaster[subsystems.ChangeSet](),
		statusBroadcaster:    newSimpleBroadcaster[interfaces.DataSynchronizerStatus](),
		quit:                 make(chan struct{}),
	}

	// Convert initial data to ChangeSet
	changeSet, err := s.makeChangeSetFromCollections(initialData)
	if err != nil {
		s.initError = err
	} else {
		s.currentChangeSet = changeSet
	}

	return s
}

func (f OfflineModeSynchronizerFactory) Build(
	ctx subsystems.ClientContext,
) (subsystems.DataSynchronizer, error) {
	return f.Synchronizer, nil
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
func (s *OfflineModeSynchronizer) Fetch(ds subsystems.DataSelector, ctx context.Context) (*subsystems.Basis, error) {
	s.mu.RLock()
	changeSet := s.currentChangeSet
	initError := s.initError
	s.mu.RUnlock()

	if initError != nil {
		return nil, initError
	}
	if changeSet == nil {
		return nil, errors.New("no data available in offline mode")
	}

	return &subsystems.Basis{
		ChangeSet: *changeSet,
		Persist:   false,
	}, nil
}

// Sync starts the synchronizer and returns a channel for receiving updates.
// For offline mode, this immediately provides the data from the archive file.
func (s *OfflineModeSynchronizer) Sync(ds subsystems.DataSelector) <-chan subsystems.DataSynchronizerResult {
	resultChan := make(chan subsystems.DataSynchronizerResult)
	changeSetChan := s.changeSetBroadcaster.AddListener()
	statusChan := s.statusBroadcaster.AddListener()

	go func() {
		defer close(resultChan)
		defer s.changeSetBroadcaster.RemoveListener(changeSetChan)
		defer s.statusBroadcaster.RemoveListener(statusChan)

		// Get initial data from stored state
		s.mu.RLock()
		changeSet := s.currentChangeSet
		initError := s.initError
		s.mu.RUnlock()

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
		resultChan <- result

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
				resultChan <- result
			case statusChange, ok := <-statusChan:
				if !ok {
					return
				}
				if statusChange.State != interfaces.DataSourceStateValid {
					result.ChangeSet = nil
				}
				result.State = statusChange.State
				result.Error = statusChange.Error
				resultChan <- result
			}
		}
	}()

	return resultChan
}

// makeChangeSetFromCollections converts old-style Collection data to a new-style ChangeSet.
// This uses the SDK's NewChangeSetFromCollections which pre-caches the collections,
// avoiding redundant conversions when the data is accessed later.
func (s *OfflineModeSynchronizer) makeChangeSetFromCollections(
	collections []ldstoretypes.Collection,
) (*subsystems.ChangeSet, error) {
	version := int(atomic.AddInt32(&s.version, 1))

	return subsystems.NewChangeSetFromCollections(
		subsystems.ServerIntent{
			Payload: subsystems.Payload{
				ID:     "",
				Target: version,
				Code:   subsystems.IntentTransferFull,
				Reason: "offline-mode-init",
			},
		},
		subsystems.NewSelector("offline", version),
		collections,
	)
}

// UpdateData allows external updates to the data (e.g., when the archive file changes).
func (s *OfflineModeSynchronizer) UpdateData(collections []ldstoretypes.Collection) error {
	changeSet, err := s.makeChangeSetFromCollections(collections)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.currentChangeSet = changeSet
	s.initError = nil // Clear any previous initialization error
	s.mu.Unlock()

	s.changeSetBroadcaster.Broadcast(*changeSet)
	return nil
}
