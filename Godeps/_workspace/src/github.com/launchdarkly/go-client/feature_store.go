package ldclient

import (
	"sync"
)

// A data structure that maintains the live
// collection of features. It is used by LaunchDarkly when streaming mode is
// enabled, and stores feature events returned by the streaming API. Custom
// FeatureStore implementations can be passed to the LaunchDarkly client via
// a custom Config object. LaunchDarkly provides two FeatureStore implementations:
// one backed by an in-memory map, and one backed by Redis.
// Implementations must be thread-safe.
type FeatureStore interface {
	Get(key string) (*Feature, error)
	All() (map[string]*Feature, error)
	Init(map[string]*Feature) error
	Delete(key string, version int) error
	Upsert(key string, f Feature) error
	Initialized() bool
}

// A memory based FeatureStore implementation, backed by a lock-striped map.
type InMemoryFeatureStore struct {
	features      map[string]*Feature
	isInitialized bool
	sync.RWMutex
}

// Creates a new in-memory FeatureStore instance.
func NewInMemoryFeatureStore() *InMemoryFeatureStore {
	return &InMemoryFeatureStore{
		features:      make(map[string]*Feature),
		isInitialized: false,
	}
}

func (store *InMemoryFeatureStore) Get(key string) (*Feature, error) {
	store.RLock()
	defer store.RUnlock()
	f := store.features[key]

	if f == nil || f.Deleted {
		return nil, nil
	} else {
		return f, nil
	}
}

func (store *InMemoryFeatureStore) All() (map[string]*Feature, error) {
	store.RLock()
	defer store.RUnlock()
	fs := make(map[string]*Feature)

	for k, v := range store.features {
		if !v.Deleted {
			fs[k] = v
		}
	}
	return fs, nil
}

func (store *InMemoryFeatureStore) Delete(key string, version int) error {
	store.Lock()
	defer store.Unlock()
	f := store.features[key]
	if f != nil && f.Version < version {
		f.Deleted = true
		f.Version = version
		store.features[key] = f
	} else if f == nil {
		f = &Feature{Deleted: true, Version: version}
		store.features[key] = f
	}
	return nil
}

func (store *InMemoryFeatureStore) Init(fs map[string]*Feature) error {
	store.Lock()
	defer store.Unlock()

	store.features = make(map[string]*Feature)

	for k, v := range fs {
		store.features[k] = v
	}
	store.isInitialized = true
	return nil
}

func (store *InMemoryFeatureStore) Upsert(key string, f Feature) error {
	store.Lock()
	defer store.Unlock()
	old := store.features[key]

	if old == nil || old.Version < f.Version {
		store.features[key] = &f
	}
	return nil
}

func (store *InMemoryFeatureStore) Initialized() bool {
	store.RLock()
	defer store.RUnlock()
	return store.isInitialized
}
