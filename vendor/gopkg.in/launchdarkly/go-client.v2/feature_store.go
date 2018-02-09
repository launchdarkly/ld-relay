package ldclient

import (
	"log"
	"os"
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
	Get(key string) (*FeatureFlag, error)
	All() (map[string]*FeatureFlag, error)
	Init(map[string]*FeatureFlag) error
	Delete(key string, version int) error
	Upsert(key string, f FeatureFlag) error
	Initialized() bool
}

// A memory based FeatureStore implementation, backed by a lock-striped map.
type InMemoryFeatureStore struct {
	features      map[string]*FeatureFlag
	isInitialized bool
	sync.RWMutex
	logger *log.Logger
}

// Creates a new in-memory FeatureStore instance.
func NewInMemoryFeatureStore(logger *log.Logger) *InMemoryFeatureStore {
	if logger == nil {
		logger = log.New(os.Stderr, "[LaunchDarkly InMemoryFeatureStore]", log.LstdFlags)
	}
	return &InMemoryFeatureStore{
		features:      make(map[string]*FeatureFlag),
		isInitialized: false,
		logger:        logger,
	}
}

func (store *InMemoryFeatureStore) Get(key string) (*FeatureFlag, error) {
	store.RLock()
	defer store.RUnlock()
	f := store.features[key]

	if f == nil {
		store.logger.Printf("WARN: Feature flag not found in store. Key: %s", key)
		return nil, nil
	} else if f.Deleted {
		store.logger.Printf("WARN: Attempted to get deleted feature flag. Key: %s", key)
		return nil, nil
	} else {
		return f, nil
	}
}

func (store *InMemoryFeatureStore) All() (map[string]*FeatureFlag, error) {
	store.RLock()
	defer store.RUnlock()
	fs := make(map[string]*FeatureFlag)

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
		f = &FeatureFlag{Deleted: true, Version: version}
		store.features[key] = f
	}
	return nil
}

func (store *InMemoryFeatureStore) Init(fs map[string]*FeatureFlag) error {
	store.Lock()
	defer store.Unlock()

	store.features = make(map[string]*FeatureFlag)

	for k, v := range fs {
		store.features[k] = v
	}
	store.isInitialized = true
	return nil
}

func (store *InMemoryFeatureStore) Upsert(key string, f FeatureFlag) error {
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
