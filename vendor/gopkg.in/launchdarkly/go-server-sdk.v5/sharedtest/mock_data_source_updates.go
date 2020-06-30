package sharedtest

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// MockDataSourceUpdates is a mock implementation of DataSourceUpdates for testing data sources.
type MockDataSourceUpdates struct {
	DataStore               *CapturingDataStore
	Statuses                chan interfaces.DataSourceStatus
	dataStoreStatusProvider *mockDataStoreStatusProvider
	lastStatus              interfaces.DataSourceStatus
	lock                    sync.Mutex
}

// NewMockDataSourceUpdates creates an instance of MockDataSourceUpdates.
//
// The DataStoreStatusProvider can be nil if we are not doing a test that requires manipulation of that
// component.
func NewMockDataSourceUpdates(realStore interfaces.DataStore) *MockDataSourceUpdates {
	dataStore := NewCapturingDataStore(realStore)
	dataStoreStatusProvider := &mockDataStoreStatusProvider{
		dataStore: dataStore,
		status:    interfaces.DataStoreStatus{Available: true},
		statusCh:  make(chan interfaces.DataStoreStatus, 10),
	}
	return &MockDataSourceUpdates{
		DataStore:               dataStore,
		Statuses:                make(chan interfaces.DataSourceStatus, 10),
		dataStoreStatusProvider: dataStoreStatusProvider,
	}
}

// Init in this test implementation, delegates to d.DataStore.CapturedUpdates.
func (d *MockDataSourceUpdates) Init(allData []interfaces.StoreCollection) bool {
	for _, coll := range allData {
		AssertNotNil(coll.Kind)
	}
	err := d.DataStore.Init(allData)
	return err == nil
}

// Upsert in this test implementation, delegates to d.DataStore.CapturedUpdates.
func (d *MockDataSourceUpdates) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	newItem interfaces.StoreItemDescriptor,
) bool {
	AssertNotNil(kind)
	_, err := d.DataStore.Upsert(kind, key, newItem)
	return err == nil
}

// UpdateStatus in this test implementation, pushes a value onto the Statuses channel.
func (d *MockDataSourceUpdates) UpdateStatus(
	newState interfaces.DataSourceState,
	newError interfaces.DataSourceErrorInfo,
) {
	d.lock.Lock()
	defer d.lock.Unlock()
	if newState != d.lastStatus.State || newError.Kind != "" {
		d.lastStatus = interfaces.DataSourceStatus{State: newState, LastError: newError}
		d.Statuses <- d.lastStatus
	}
}

// GetDataStoreStatusProvider returns a stub implementation that does not have full functionality
// but enough to test a data source with.
func (d *MockDataSourceUpdates) GetDataStoreStatusProvider() interfaces.DataStoreStatusProvider {
	return d.dataStoreStatusProvider
}

// UpdateStoreStatus simulates a change in the data store status.
func (d *MockDataSourceUpdates) UpdateStoreStatus(newStatus interfaces.DataStoreStatus) {
	d.dataStoreStatusProvider.statusCh <- newStatus
}

// RequireStatusOf blocks until a new data source status is available, and verifies its state.
func (d *MockDataSourceUpdates) RequireStatusOf(
	t *testing.T,
	newState interfaces.DataSourceState,
) interfaces.DataSourceStatus {
	status := d.RequireStatus(t)
	assert.Equal(t, string(newState), string(status.State))
	// string conversion is due to a bug in assert with type aliases
	return status
}

// RequireStatus blocks until a new data source status is available.
func (d *MockDataSourceUpdates) RequireStatus(t *testing.T) interfaces.DataSourceStatus {
	select {
	case s := <-d.Statuses:
		return s
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for new data source status")
		return interfaces.DataSourceStatus{}
	}
}

type mockDataStoreStatusProvider struct {
	dataStore interfaces.DataStore
	status    interfaces.DataStoreStatus
	statusCh  chan interfaces.DataStoreStatus
	lock      sync.Mutex
}

func (m *mockDataStoreStatusProvider) GetStatus() interfaces.DataStoreStatus {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.status
}

func (m *mockDataStoreStatusProvider) IsStatusMonitoringEnabled() bool {
	return m.dataStore.IsStatusMonitoringEnabled()
}

func (m *mockDataStoreStatusProvider) AddStatusListener() <-chan interfaces.DataStoreStatus {
	return m.statusCh
}

func (m *mockDataStoreStatusProvider) RemoveStatusListener(ch <-chan interfaces.DataStoreStatus) {
}
