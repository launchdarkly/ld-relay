package store

import (
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockInitChecker struct {
	mu        sync.Mutex
	available bool
	inited    bool
	err       error
}

func (m *mockInitChecker) CheckInitialized() (available bool, initialized bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available, m.inited, m.err
}

func (m *mockInitChecker) set(available, inited bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = available
	m.inited = inited
	m.err = err
}

func makeHealthCheckTestComponents() (*mockStore, *streamUpdatesStoreWrapper, *mockInitChecker, *mockEnvStreamsUpdates) {
	baseStore, wrappedStore, updates := makeTestComponents()
	checker := &mockInitChecker{available: true, inited: true}
	return baseStore, wrappedStore, checker, updates
}

func TestHealthCheckDetectsDataLossAndRepopulates(t *testing.T) {
	baseStore, wrappedStore, checker, _ := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Clear the base store to simulate Redis data loss
	_ = baseStore.Init(nil)

	// Redis is up but $inited is gone (data loss after restart)
	checker.set(true, false, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	// Health check should trigger repopulation - verify data comes back
	assert.Eventually(t, func() bool {
		flags, e := baseStore.GetAll(ldstoreimpl.Features())
		return e == nil && len(flags) > 0
	}, time.Second, 5*time.Millisecond, "health check should repopulate the store")
}

func TestHealthCheckClearsCircuitBreaker(t *testing.T) {
	_, wrappedStore, checker, _ := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	wrappedStore.SetStoreDown(true)
	checker.set(true, true, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	assert.Eventually(t, func() bool {
		return !wrappedStore.IsStoreDown()
	}, time.Second, 5*time.Millisecond, "health check should clear circuit breaker")
}

func TestHealthCheckDoesNothingOnConnectionError(t *testing.T) {
	_, wrappedStore, checker, _ := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	wrappedStore.SetStoreDown(true)
	checker.set(false, false, fakeError)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	time.Sleep(50 * time.Millisecond)
	assert.True(t, wrappedStore.IsStoreDown(), "circuit breaker should remain open on connection error")
}

func TestHealthCheckDoesNotRepopulateWithoutSnapshot(t *testing.T) {
	_, wrappedStore, checker, updates := makeHealthCheckTestComponents()
	// No Init called, no snapshot
	checker.set(true, false, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	time.Sleep(50 * time.Millisecond)
	updates.expectNoAllDataUpdate(t)
}

func TestHealthCheckStops(t *testing.T) {
	_, wrappedStore, checker, _ := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)
	checker.set(true, true, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	hc.Stop()
	// Verify Stop doesn't panic on double-call
	hc.Stop()
}

func TestHealthCheckNilParams(t *testing.T) {
	hc := NewStoreHealthCheck(nil, nil, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	assert.Nil(t, hc, "health check should not be created without store and checker")
}

func TestHealthCheckRepopulationIncludesUpserts(t *testing.T) {
	baseStore, wrappedStore, checker, _ := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)

	// Upsert a new flag after Init
	testFlag2Desc := ldstoretypes.ItemDescriptor{Version: testFlag2.Version, Item: &testFlag2}
	_, _ = wrappedStore.Upsert(ldstoreimpl.Features(), testFlag2.Key, testFlag2Desc)

	// Clear store to simulate Redis restart
	_ = baseStore.Init(nil)

	// Simulate Redis data loss
	checker.set(true, false, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	// Wait for repopulation
	assert.Eventually(t, func() bool {
		flags, e := baseStore.GetAll(ldstoreimpl.Features())
		return e == nil && len(flags) == 2
	}, time.Second, 5*time.Millisecond, "repopulation should include upserted flags")
}

func TestHealthCheckNormalOperationDoesNothing(t *testing.T) {
	_, wrappedStore, checker, updates := makeHealthCheckTestComponents()
	err := wrappedStore.Init(allData)
	require.NoError(t, err)
	// Reset the updates tracker since Init sends an update
	updates.allData = nil

	// Everything is fine
	checker.set(true, true, nil)

	hc := NewStoreHealthCheck(wrappedStore, checker, 10*time.Millisecond, ldlog.NewDisabledLoggers())
	require.NotNil(t, hc)
	hc.Start()
	defer hc.Stop()

	time.Sleep(50 * time.Millisecond)
	// No repopulation should have been triggered
	updates.expectNoAllDataUpdate(t)
	assert.False(t, wrappedStore.IsStoreDown())
}
