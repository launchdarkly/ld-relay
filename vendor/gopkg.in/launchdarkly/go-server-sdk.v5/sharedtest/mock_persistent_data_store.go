package sharedtest

import (
	"sync"
	"time"

	intf "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

type mockDatabaseInstance struct { //nolint:unused // it is used in test code
	dataByPrefix   map[string]map[intf.StoreDataKind]map[string]intf.StoreSerializedItemDescriptor
	initedByPrefix map[string]*bool
}

func newMockDatabaseInstance() *mockDatabaseInstance { //nolint:deadcode,unused // it is used in test code
	return &mockDatabaseInstance{
		dataByPrefix:   make(map[string]map[intf.StoreDataKind]map[string]intf.StoreSerializedItemDescriptor),
		initedByPrefix: make(map[string]*bool),
	}
}

func (db *mockDatabaseInstance) Clear(prefix string) {
	for _, m := range db.dataByPrefix[prefix] {
		for k := range m {
			delete(m, k)
		}
	}
	if v, ok := db.initedByPrefix[prefix]; ok {
		*v = false
	}
}

// MockPersistentDataStore is a test implementation of PersistentDataStore.
type MockPersistentDataStore struct {
	data                map[intf.StoreDataKind]map[string]intf.StoreSerializedItemDescriptor
	persistOnlyAsString bool
	fakeError           error
	available           bool
	inited              *bool
	InitQueriedCount    int
	queryDelay          time.Duration
	queryStartedCh      chan struct{}
	testTxHook          func()
	closed              bool
	lock                sync.Mutex
}

func newData() map[intf.StoreDataKind]map[string]intf.StoreSerializedItemDescriptor {
	return map[intf.StoreDataKind]map[string]intf.StoreSerializedItemDescriptor{
		MockData:      {},
		MockOtherData: {},
	}
}

// NewMockPersistentDataStore creates a test implementation of a persistent data store.
func NewMockPersistentDataStore() *MockPersistentDataStore {
	f := false
	m := &MockPersistentDataStore{data: newData(), inited: &f, available: true}
	return m
}

//nolint:deadcode,unused // it is used in test code
func newMockPersistentDataStoreWithPrefix(
	db *mockDatabaseInstance,
	prefix string,
) *MockPersistentDataStore {
	m := &MockPersistentDataStore{available: true}
	if _, ok := db.dataByPrefix[prefix]; !ok {
		db.dataByPrefix[prefix] = newData()
		f := false
		db.initedByPrefix[prefix] = &f
	}
	m.data = db.dataByPrefix[prefix]
	m.inited = db.initedByPrefix[prefix]
	return m
}

// EnableInstrumentedQueries puts the test store into a mode where all get operations begin by posting
// a signal to a channel and then waiting for some amount of time, to test coalescing of requests.
func (m *MockPersistentDataStore) EnableInstrumentedQueries(queryDelay time.Duration) <-chan struct{} {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.queryDelay = queryDelay
	m.queryStartedCh = make(chan struct{}, 10)
	return m.queryStartedCh
}

// ForceGet retrieves a serialized item directly from the test data with no other processing.
func (m *MockPersistentDataStore) ForceGet(kind intf.StoreDataKind, key string) intf.StoreSerializedItemDescriptor {
	m.lock.Lock()
	defer m.lock.Unlock()
	if ret, ok := m.data[kind][key]; ok {
		return ret
	}
	return intf.StoreSerializedItemDescriptor{}.NotFound()
}

// ForceSet directly modifies an item in the test data.
func (m *MockPersistentDataStore) ForceSet(
	kind intf.StoreDataKind,
	key string,
	item intf.StoreSerializedItemDescriptor,
) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.data[kind][key] = item
}

// ForceRemove deletes an item from the test data.
func (m *MockPersistentDataStore) ForceRemove(kind intf.StoreDataKind, key string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	delete(m.data[kind], key)
}

// ForceSetInited changes the value that will be returned by IsInitialized().
func (m *MockPersistentDataStore) ForceSetInited(inited bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	*m.inited = inited
}

// SetAvailable changes the value that will be returned by IsStoreAvailable().
func (m *MockPersistentDataStore) SetAvailable(available bool) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.available = available
}

// SetFakeError causes subsequent store operations to return an error.
func (m *MockPersistentDataStore) SetFakeError(fakeError error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.fakeError = fakeError
}

func (m *MockPersistentDataStore) startQuery() {
	if m.queryStartedCh != nil {
		m.queryStartedCh <- struct{}{}
	}
	if m.queryDelay > 0 {
		<-time.After(m.queryDelay)
	}
}

// Init is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) Init(allData []intf.StoreSerializedCollection) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.fakeError != nil {
		return m.fakeError
	}
	for _, mm := range m.data {
		for k := range mm {
			delete(mm, k)
		}
	}
	for _, coll := range allData {
		AssertNotNil(coll.Kind)
		itemsMap := make(map[string]intf.StoreSerializedItemDescriptor)
		for _, item := range coll.Items {
			itemsMap[item.Key] = m.storableItem(item.Item)
		}
		m.data[coll.Kind] = itemsMap
	}
	*m.inited = true
	return nil
}

// Get is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) Get(kind intf.StoreDataKind, key string) (intf.StoreSerializedItemDescriptor, error) {
	AssertNotNil(kind)
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.fakeError != nil {
		return intf.StoreSerializedItemDescriptor{}.NotFound(), m.fakeError
	}
	m.startQuery()
	if item, ok := m.data[kind][key]; ok {
		return m.retrievedItem(item), nil
	}
	return intf.StoreSerializedItemDescriptor{}.NotFound(), nil
}

// GetAll is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) GetAll(kind intf.StoreDataKind) ([]intf.StoreKeyedSerializedItemDescriptor, error) {
	AssertNotNil(kind)
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.fakeError != nil {
		return nil, m.fakeError
	}
	m.startQuery()
	ret := []intf.StoreKeyedSerializedItemDescriptor{}
	for k, v := range m.data[kind] {
		ret = append(ret, intf.StoreKeyedSerializedItemDescriptor{Key: k, Item: m.retrievedItem(v)})
	}
	return ret, nil
}

// Upsert is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) Upsert(
	kind intf.StoreDataKind,
	key string,
	newItem intf.StoreSerializedItemDescriptor,
) (bool, error) {
	AssertNotNil(kind)
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.fakeError != nil {
		return false, m.fakeError
	}
	if m.testTxHook != nil {
		m.testTxHook()
	}
	if oldItem, ok := m.data[kind][key]; ok {
		oldVersion := oldItem.Version
		if m.persistOnlyAsString {
			// If persistOnlyAsString is true, simulate the kind of implementation where we can't see the
			// version as a separate attribute in the database and must deserialize the item to get it.
			oldDeserializedItem, _ := kind.Deserialize(oldItem.SerializedItem)
			oldVersion = oldDeserializedItem.Version
		}
		if oldVersion >= newItem.Version {
			return false, nil
		}
	}
	m.data[kind][key] = m.storableItem(newItem)
	return true, nil
}

// IsInitialized is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) IsInitialized() bool {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.InitQueriedCount++
	return *m.inited
}

// IsStoreAvailable is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) IsStoreAvailable() bool {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.available
}

// Close is a standard PersistentDataStore method.
func (m *MockPersistentDataStore) Close() error {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.closed = true
	return nil
}

func (m *MockPersistentDataStore) retrievedItem(
	item intf.StoreSerializedItemDescriptor,
) intf.StoreSerializedItemDescriptor {
	if m.persistOnlyAsString {
		// This simulates the kind of store implementation that can't track metadata separately
		return intf.StoreSerializedItemDescriptor{Version: 0, SerializedItem: item.SerializedItem}
	}
	return item
}

func (m *MockPersistentDataStore) storableItem(
	item intf.StoreSerializedItemDescriptor,
) intf.StoreSerializedItemDescriptor {
	if item.Deleted && !m.persistOnlyAsString {
		// This simulates the kind of store implementation that *can* track metadata separately, so we don't
		// have to persist the placeholder string for deleted items
		return intf.StoreSerializedItemDescriptor{Version: item.Version, Deleted: true}
	}
	return item
}
