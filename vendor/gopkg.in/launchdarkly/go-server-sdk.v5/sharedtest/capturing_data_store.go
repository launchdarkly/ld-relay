package sharedtest

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-test-helpers/ldservices"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

type upsertParams struct {
	kind interfaces.StoreDataKind
	key  string
	item interfaces.StoreItemDescriptor
}

// CapturingDataStore is a DataStore implementation that records update operations for testing.
type CapturingDataStore struct {
	realStore               interfaces.DataStore
	statusMonitoringEnabled bool
	fakeError               error
	inits                   chan []interfaces.StoreCollection
	upserts                 chan upsertParams
	lock                    sync.Mutex
}

// NewCapturingDataStore creates an instance of CapturingDataStore.
func NewCapturingDataStore(realStore interfaces.DataStore) *CapturingDataStore {
	return &CapturingDataStore{
		realStore:               realStore,
		inits:                   make(chan []interfaces.StoreCollection, 10),
		upserts:                 make(chan upsertParams, 10),
		statusMonitoringEnabled: true,
	}
}

// Init is a standard DataStore method.
func (d *CapturingDataStore) Init(allData []interfaces.StoreCollection) error {
	for _, coll := range allData {
		AssertNotNil(coll.Kind)
	}
	d.inits <- allData
	_ = d.realStore.Init(allData)
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.fakeError
}

// Get is a standard DataStore method.
func (d *CapturingDataStore) Get(kind interfaces.StoreDataKind, key string) (interfaces.StoreItemDescriptor, error) {
	AssertNotNil(kind)
	if d.fakeError != nil {
		return interfaces.StoreItemDescriptor{}.NotFound(), d.fakeError
	}
	return d.realStore.Get(kind, key)
}

// GetAll is a standard DataStore method.
func (d *CapturingDataStore) GetAll(kind interfaces.StoreDataKind) ([]interfaces.StoreKeyedItemDescriptor, error) {
	AssertNotNil(kind)
	if d.fakeError != nil {
		return nil, d.fakeError
	}
	return d.realStore.GetAll(kind)
}

// Upsert in this test type does nothing but capture its parameters.
func (d *CapturingDataStore) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	newItem interfaces.StoreItemDescriptor,
) (bool, error) {
	AssertNotNil(kind)
	d.upserts <- upsertParams{kind, key, newItem}
	updated, _ := d.realStore.Upsert(kind, key, newItem)
	d.lock.Lock()
	defer d.lock.Unlock()
	return updated, d.fakeError
}

// IsInitialized in this test type always returns true.
func (d *CapturingDataStore) IsInitialized() bool {
	return true
}

// IsStatusMonitoringEnabled in this test type returns true by default, but can be changed
// with SetStatusMonitoringEnabled.
func (d *CapturingDataStore) IsStatusMonitoringEnabled() bool {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.statusMonitoringEnabled
}

// Close in this test type is a no-op.
func (d *CapturingDataStore) Close() error {
	return nil
}

// SetStatusMonitoringEnabled changes the value returned by IsStatusMonitoringEnabled.
func (d *CapturingDataStore) SetStatusMonitoringEnabled(statusMonitoringEnabled bool) {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.statusMonitoringEnabled = statusMonitoringEnabled
}

// SetFakeError causes subsequent Init or Upsert calls to return an error.
func (d *CapturingDataStore) SetFakeError(fakeError error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.fakeError = fakeError
}

// WaitForNextInit waits for an Init call.
func (d *CapturingDataStore) WaitForNextInit(
	t *testing.T,
	timeout time.Duration,
) []interfaces.StoreCollection {
	select {
	case inited := <-d.inits:
		return inited
	case <-time.After(timeout):
		require.Fail(t, "timed out before receiving expected init")
	}
	return nil
}

// WaitForInit waits for an Init call and verifies that it matches the expected data.
func (d *CapturingDataStore) WaitForInit(
	t *testing.T,
	data *ldservices.ServerSDKData,
	timeout time.Duration,
) {
	select {
	case inited := <-d.inits:
		assertReceivedInitDataEquals(t, data, inited)
		break
	case <-time.After(timeout):
		require.Fail(t, "timed out before receiving expected init")
	}
}

// WaitForUpsert waits for an Upsert call and verifies that it matches the expected data.
func (d *CapturingDataStore) WaitForUpsert(
	t *testing.T,
	kind interfaces.StoreDataKind,
	key string,
	version int,
	timeout time.Duration,
) {
	select {
	case upserted := <-d.upserts:
		assert.Equal(t, key, upserted.key)
		assert.Equal(t, version, upserted.item.Version)
		assert.NotNil(t, upserted.item.Item)
		break
	case <-time.After(timeout):
		require.Fail(t, "timed out before receiving expected update")
	}
}

// WaitForDelete waits for an Upsert call that is expected to delete a data item.
func (d *CapturingDataStore) WaitForDelete(
	t *testing.T,
	kind interfaces.StoreDataKind,
	key string,
	version int,
	timeout time.Duration,
) {
	select {
	case upserted := <-d.upserts:
		assert.Equal(t, key, upserted.key)
		assert.Equal(t, version, upserted.item.Version)
		assert.Nil(t, upserted.item.Item)
		break
	case <-time.After(timeout):
		require.Fail(t, "timed out before receiving expected deletion")
	}
}

func assertReceivedInitDataEquals(
	t *testing.T,
	expected *ldservices.ServerSDKData,
	received []interfaces.StoreCollection,
) {
	assert.Equal(t, 2, len(received))
	for _, coll := range received {
		var itemsMap map[string]interface{}
		switch coll.Kind {
		case interfaces.DataKindFeatures():
			itemsMap = expected.FlagsMap
		case interfaces.DataKindSegments():
			itemsMap = expected.SegmentsMap
		default:
			assert.Fail(t, "received unknown data kind: %s", coll.Kind)
		}
		assert.Equal(t, len(itemsMap), len(coll.Items))
		for _, item := range coll.Items {
			found, ok := itemsMap[item.Key]
			assert.True(t, ok, item.Key)
			bytes, _ := json.Marshal(found)
			var props map[string]interface{}
			assert.NoError(t, json.Unmarshal(bytes, &props))
			assert.Equal(t, props["version"].(float64), float64(item.Item.Version))
		}
	}
}
