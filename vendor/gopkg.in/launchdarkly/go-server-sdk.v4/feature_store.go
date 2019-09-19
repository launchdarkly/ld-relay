package ldclient

import (
	"sync"

	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

// FeatureStore is an interface describing a structure that maintains the live collection of features
// and related objects. Whenever the SDK retrieves feature flag data from LaunchDarkly, via streaming
// or polling, it puts the data into the FeatureStore; then it queries the store whenever a flag needs
// to be evaluated. Therefore, implementations must be thread-safe.
//
// The SDK provides a default in-memory implementation (NewInMemoryFeatureStore), as well as database
// integrations in the "redis", "ldconsul", and "lddynamodb" packages. To use an implementation other
// than the default, put an instance of it in the FeatureStore property of your client configuration.
//
// If you want to create a custom implementation, it may be helpful to use the FeatureStoreWrapper
// type in the utils package; this provides commonly desired behaviors such as caching. Custom
// implementations must be able to handle any objects that implement the VersionedData interface,
// so if they need to marshal objects, the marshaling must be reflection-based. The VersionedDataKind
// type provides the necessary metadata to support this.
type FeatureStore interface {
	// Get attempts to retrieve an item of the specified kind from the data store using its unique key.
	// If no such item exists, it returns nil. If the item exists but has a Deleted property that is true,
	// it returns nil.
	Get(kind VersionedDataKind, key string) (VersionedData, error)
	// All retrieves all items of the specified kind from the data store, returning a map of keys to
	// items. Any items whose Deleted property is true must be omitted. If the store is empty, it
	// returns an empty map.
	All(kind VersionedDataKind) (map[string]VersionedData, error)
	// Init performs an update of the entire data store, replacing any existing data.
	Init(data map[VersionedDataKind]map[string]VersionedData) error
	// Delete removes the specified item from the data store, unless its Version property is greater
	// than or equal to the specified version, in which case nothing happens. Removal should be done
	// by storing an item whose Deleted property is true (use VersionedDataKind.MakeDeleteItem()).
	Delete(kind VersionedDataKind, key string, version int) error
	// Upsert adds or updates the specified item, unless the existing item in the store has a Version
	// property greater than or equal to the new item's Version, in which case nothing happens.
	Upsert(kind VersionedDataKind, item VersionedData) error
	// Initialized returns true if the data store contains a data set, meaning that Init has been
	// called at least once. In a shared data store, it should be able to detect this even if Init
	// was called in a different process, i.e. the test should be based on looking at what is in
	// the data store. Once this has been determined to be true, it can continue to return true
	// without having to check the store again; this method should be as fast as possible since it
	// may be called during feature flag evaluations.
	Initialized() bool
}

// FeatureStoreFactory is a factory function that produces a FeatureStore implementation. It receives
// a copy of the Config so that it can use the same logging configuration as the rest of the SDK; it
// can assume that config.Loggers has been initialized so it can write to any log level.
type FeatureStoreFactory func(config Config) (FeatureStore, error)

// InMemoryFeatureStore is a memory based FeatureStore implementation, backed by a lock-striped map.
type InMemoryFeatureStore struct {
	allData       map[VersionedDataKind]map[string]VersionedData
	isInitialized bool
	sync.RWMutex
	loggers ldlog.Loggers
}

// NewInMemoryFeatureStore creates a new in-memory FeatureStore instance.
//
// Deprecated: Specific implementation types such as InMemoryFeatureStore should not be used and
// may be removed in the future. Instead, use NewInMemoryFeatureStoreFactory.
func NewInMemoryFeatureStore(logger Logger) *InMemoryFeatureStore {
	config := Config{}
	config.Loggers.SetBaseLogger(logger)
	return newInMemoryFeatureStoreInternal(config)
}

// NewInMemoryFeatureStoreFactory returns a factory function to create an in-memory FeatureStore.
// Setting the FeatureStoreFactory option in Config to this function ensures that it will use the
// same logging configuration as the other SDK components.
func NewInMemoryFeatureStoreFactory() FeatureStoreFactory {
	return func(config Config) (FeatureStore, error) {
		return newInMemoryFeatureStoreInternal(config), nil
	}
}

func newInMemoryFeatureStoreInternal(config Config) *InMemoryFeatureStore {
	loggers := config.Loggers
	loggers.SetPrefix("InMemoryFeatureStore:")
	return &InMemoryFeatureStore{
		allData:       make(map[VersionedDataKind]map[string]VersionedData),
		isInitialized: false,
		loggers:       loggers,
	}
}

// Get returns an individual object of a given type from the store
func (store *InMemoryFeatureStore) Get(kind VersionedDataKind, key string) (VersionedData, error) {
	store.RLock()
	defer store.RUnlock()
	if store.allData[kind] == nil {
		store.allData[kind] = make(map[string]VersionedData)
	}
	item := store.allData[kind][key]

	if item == nil {
		store.loggers.Debugf(`Key %s not found in "%s"`, key, kind)
		return nil, nil
	} else if item.IsDeleted() {
		store.loggers.Debugf(`Attempted to get deleted item with key %s in "%s"`, kind, key)
		return nil, nil
	} else {
		return item, nil
	}
}

// All returns all the objects of a given kind from the store
func (store *InMemoryFeatureStore) All(kind VersionedDataKind) (map[string]VersionedData, error) {
	store.RLock()
	defer store.RUnlock()
	ret := make(map[string]VersionedData)

	for k, v := range store.allData[kind] {
		if !v.IsDeleted() {
			ret[k] = v
		}
	}
	return ret, nil
}

// Delete removes an item of a given kind from the store
func (store *InMemoryFeatureStore) Delete(kind VersionedDataKind, key string, version int) error {
	store.Lock()
	defer store.Unlock()
	if store.allData[kind] == nil {
		store.allData[kind] = make(map[string]VersionedData)
	}
	items := store.allData[kind]
	item := items[key]
	if item == nil || item.GetVersion() < version {
		deletedItem := kind.MakeDeletedItem(key, version)
		items[key] = deletedItem
	}
	return nil
}

// Init populates the store with a complete set of versioned data
func (store *InMemoryFeatureStore) Init(allData map[VersionedDataKind]map[string]VersionedData) error {
	store.Lock()
	defer store.Unlock()

	store.allData = make(map[VersionedDataKind]map[string]VersionedData)

	for k, v := range allData {
		items := make(map[string]VersionedData)
		for k1, v1 := range v {
			items[k1] = v1
		}
		store.allData[k] = items
	}

	store.isInitialized = true
	return nil
}

// Upsert inserts or replaces an item in the store unless there it already contains an item with an equal or larger version
func (store *InMemoryFeatureStore) Upsert(kind VersionedDataKind, item VersionedData) error {
	store.Lock()
	defer store.Unlock()
	if store.allData[kind] == nil {
		store.allData[kind] = make(map[string]VersionedData)
	}
	items := store.allData[kind]
	old := items[item.GetKey()]

	if old == nil || old.GetVersion() < item.GetVersion() {
		items[item.GetKey()] = item
	}
	return nil
}

// Initialized returns whether the store has been initialized with data
func (store *InMemoryFeatureStore) Initialized() bool {
	store.RLock()
	defer store.RUnlock()
	return store.isInitialized
}
