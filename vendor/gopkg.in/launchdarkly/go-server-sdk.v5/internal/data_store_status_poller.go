package internal

import (
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// dataStoreStatusPoller maintains the "last known available" state for a persistent data store and
// can poll the store for recovery. This is used only by persistentDataStoreWrapper.
type dataStoreStatusPoller struct {
	statusUpdater     func(interfaces.DataStoreStatus)
	lock              sync.Mutex
	lastAvailable     bool
	pollFn            func() bool
	refreshOnRecovery bool
	pollCloser        chan struct{}
	closeOnce         sync.Once
	loggers           ldlog.Loggers
}

const statusPollInterval = time.Millisecond * 500

// newDataStoreStatusPoller creates a new dataStoreStatusPoller. The pollFn should return
// true if the store is available, false if not.
func newDataStoreStatusPoller(
	availableNow bool,
	pollFn func() bool,
	statusUpdater func(interfaces.DataStoreStatus),
	refreshOnRecovery bool,
	loggers ldlog.Loggers,
) *dataStoreStatusPoller {
	return &dataStoreStatusPoller{
		lastAvailable:     availableNow,
		pollFn:            pollFn,
		statusUpdater:     statusUpdater,
		refreshOnRecovery: refreshOnRecovery,
		loggers:           loggers,
	}
}

// UpdateAvailability signals that the store is now available or unavailable. If that is a change,
// an update will be sent (and, if the new status is unavailable, it will start polling for recovery).
func (m *dataStoreStatusPoller) UpdateAvailability(available bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if available == m.lastAvailable {
		return
	}
	m.lastAvailable = available
	newStatus := interfaces.DataStoreStatus{Available: available}
	if available {
		m.loggers.Warn("Persistent store is available again")
		newStatus.NeedsRefresh = m.refreshOnRecovery
	}
	m.statusUpdater(newStatus)

	// If the store has just become unavailable, start a poller to detect when it comes back.
	if !available {
		m.loggers.Warn("Detected persistent store unavailability; updates will be cached until it recovers")
		// Start a goroutine to poll until the store starts working again or we shut down.
		m.pollCloser = m.startStatusPoller()
	}
}

// Close shuts down all channels and goroutines used by the manager.
func (m *dataStoreStatusPoller) Close() {
	m.closeOnce.Do(func() {
		if m.pollCloser != nil {
			close(m.pollCloser)
			m.pollCloser = nil
		}
	})
}

func (m *dataStoreStatusPoller) startStatusPoller() chan struct{} {
	closer := make(chan struct{})
	go func() {
		ticker := time.NewTicker(statusPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if m.pollFn() {
					m.UpdateAvailability(true)
					return
				}
			case <-closer:
				return
			}
		}
	}()
	return closer
}
