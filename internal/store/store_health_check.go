package store

import (
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// StoreInitChecker abstracts the ability to check whether a persistent store still
// contains its initialization marker (e.g. the Redis $inited key). Implementations
// should query the store directly, bypassing any SDK-level caching.
type StoreInitChecker interface {
	// CheckInitialized returns:
	//   available=true, initialized=true  → store is up and has data
	//   available=true, initialized=false → store is up but data is missing (needs repopulation)
	//   available=false with err          → store is unreachable (retry later)
	CheckInitialized() (available bool, initialized bool, err error)
}

// SnapshotStore is the interface that StoreHealthCheck uses to interact with the store wrapper.
// It provides access to snapshot data and circuit breaker state for resilience operations.
type SnapshotStore interface {
	HasSnapshot() bool
	GetSnapshot() []ldstoretypes.Collection
	IsStoreDown() bool
	SetStoreDown(bool)
	RepopulateStore([]ldstoretypes.Collection) error
	IsInitialized() bool
}

// StoreHealthCheck periodically verifies that the persistent data store still contains
// its initialization data. If data loss is detected (e.g. after a Redis restart), it
// repopulates the store from the in-memory snapshot and manages the circuit breaker state.
type StoreHealthCheck struct {
	store    SnapshotStore
	checker  StoreInitChecker
	interval time.Duration
	loggers  ldlog.Loggers
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewStoreHealthCheck creates a new health check instance. Returns nil if store or checker is nil.
func NewStoreHealthCheck(
	store SnapshotStore,
	checker StoreInitChecker,
	interval time.Duration,
	loggers ldlog.Loggers,
) *StoreHealthCheck {
	if store == nil || checker == nil {
		return nil
	}
	return &StoreHealthCheck{
		store:    store,
		checker:  checker,
		interval: interval,
		loggers:  loggers,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the periodic health check in a background goroutine.
func (hc *StoreHealthCheck) Start() {
	go hc.run()
}

// Stop terminates the health check goroutine. Safe to call multiple times.
func (hc *StoreHealthCheck) Stop() {
	hc.stopOnce.Do(func() {
		close(hc.stopCh)
	})
}

func (hc *StoreHealthCheck) run() {
	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hc.check()
		case <-hc.stopCh:
			return
		}
	}
}

func (hc *StoreHealthCheck) check() {
	available, initialized, err := hc.checker.CheckInitialized()

	if err != nil {
		hc.loggers.Debugf("Data store health check: connection error: %s", err)
		return
	}

	if !available {
		hc.loggers.Debug("Data store health check: store not available")
		return
	}

	// Store is available
	if initialized {
		if hc.store.IsStoreDown() {
			hc.loggers.Info("Data store has recovered, resuming normal reads")
			hc.store.SetStoreDown(false)
		}
		return
	}

	// Store is available but not initialized -- data was lost
	hc.loggers.Warn("Data store lost initialization data, possible store restart detected")

	if !hc.store.HasSnapshot() {
		hc.loggers.Warn("Cannot repopulate data store: no snapshot data available to restore")
		return
	}

	hc.repopulate()
}

func (hc *StoreHealthCheck) repopulate() {
	snapshot := hc.store.GetSnapshot()
	if snapshot == nil {
		return
	}

	hc.loggers.Warn("Repopulating data store from in-memory snapshot")

	err := hc.store.RepopulateStore(snapshot)
	if err != nil {
		hc.loggers.Errorf("Failed to repopulate data store from snapshot: %s", err)
		return
	}

	hc.loggers.Info("Successfully repopulated data store from snapshot")

	if hc.store.IsStoreDown() {
		hc.store.SetStoreDown(false)
		hc.loggers.Info("Circuit breaker cleared after repopulation")
	}
}
