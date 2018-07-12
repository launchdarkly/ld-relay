package ldclient

import (
	"log"
	"os"
	"sync"
)

// FeatureStore is an interface describing a structure that maintains the live collection of features and related objects.
// It is used by LaunchDarkly when streaming mode is enabled, and stores data returned
// by the streaming API. Custom FeatureStore implementations can be passed to the
// LaunchDarkly client via a custom Config object. LaunchDarkly provides two FeatureStore
// implementations: one backed by an in-memory map, and one backed by Redis.
// Implementations must be thread-safe.
type FeatureStore interface {
	Get(kind VersionedDataKind, key string) (VersionedData, error)
	All(kind VersionedDataKind) (map[string]VersionedData, error)
	Init(map[VersionedDataKind]map[string]VersionedData) error
	Delete(kind VersionedDataKind, key string, version int) error
	Upsert(kind VersionedDataKind, item VersionedData) error
	Initialized() bool
}

// InMemoryFeatureStore is a memory based FeatureStore implementation, backed by a lock-striped map.
type InMemoryFeatureStore struct {
	allData       map[VersionedDataKind]map[string]VersionedData
	isInitialized bool
	sync.RWMutex
	logger Logger
}

// NewInMemoryFeatureStore creates a new in-memory FeatureStore instance.
func NewInMemoryFeatureStore(logger Logger) *InMemoryFeatureStore {
	if logger == nil {
		logger = log.New(os.Stderr, "[LaunchDarkly InMemoryFeatureStore]", log.LstdFlags)
	}
	return &InMemoryFeatureStore{
		allData:       make(map[VersionedDataKind]map[string]VersionedData),
		isInitialized: false,
		logger:        logger,
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
		store.logger.Printf("WARN: Key: %s not found in \"%s\".", key, kind)
		return nil, nil
	} else if item.IsDeleted() {
		store.logger.Printf("WARN: Attempted to get deleted item in \"%s\". Key: %s", kind, key)
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
