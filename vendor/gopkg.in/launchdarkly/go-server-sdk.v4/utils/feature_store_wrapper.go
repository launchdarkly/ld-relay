// Package utils contains support code that most users of the SDK will not need to access
// directly. However, they may be useful for anyone developing custom integrations.
package utils

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	cache "github.com/patrickmn/go-cache"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

// UnmarshalItem attempts to unmarshal an entity that has been stored as JSON in a
// FeatureStore. The kind parameter indicates what type of entity is expected.
func UnmarshalItem(kind ld.VersionedDataKind, raw []byte) (ld.VersionedData, error) {
	data := kind.GetDefaultItem()
	if jsonErr := json.Unmarshal(raw, &data); jsonErr != nil {
		return nil, jsonErr
	}
	if item, ok := data.(ld.VersionedData); ok {
		return item, nil
	}
	return nil, fmt.Errorf("unexpected data type from JSON unmarshal: %T", data)
}

// FeatureStoreCoreBase defines methods that are common to the FeatureStoreCore and
// NonAtomicFeatureStoreCore interfaces.
type FeatureStoreCoreBase interface {
	// GetInternal queries a single item from the data store. The kind parameter distinguishes
	// between different categories of data (flags, segments) and the key is the unique key
	// within that category. If no such item exists, the method should return (nil, nil).
	// It should not attempt to filter out any items based on their Deleted property, nor to
	// cache any items.
	GetInternal(kind ld.VersionedDataKind, key string) (ld.VersionedData, error)
	// GetAllInternal queries all items in a given category from the data store, returning
	// a map of unique keys to items. It should not attempt to filter out any items based
	// on their Deleted property, nor to cache any items.
	GetAllInternal(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error)
	// UpsertInternal adds or updates a single item. If an item with the same key already
	// exists, it should update it only if the new item's GetVersion() value is greater
	// than the old one. It should return the final state of the item, i.e. if the update
	// succeeded then it returns the item that was passed in, and if the update failed due
	// to the version check then it returns the item that is currently in the data store
	// (this ensures that caching works correctly).
	//
	// Note that deletes are implemented by using UpsertInternal to store an item whose
	// Deleted property is true.
	UpsertInternal(kind ld.VersionedDataKind, item ld.VersionedData) (ld.VersionedData, error)
	// InitializedInternal returns true if the data store contains a complete data set,
	// meaning that InitInternal has been called at least once. In a shared data store, it
	// should be able to detect this even if InitInternal was called in a different process,
	// i.e. the test should be based on looking at what is in the data store. The method
	// does not need to worry about caching this value; FeatureStoreWrapper will only call
	// it when necessary.
	InitializedInternal() bool
	// GetCacheTTL returns the length of time that data should be retained in an in-memory
	// cache. This cache is maintained by FeatureStoreWrapper. If GetCacheTTL returns zero,
	// there will be no cache.
	GetCacheTTL() time.Duration
}

// FeatureStoreCore is an interface for a simplified subset of the functionality of
// ldclient.FeatureStore, to be used in conjunction with FeatureStoreWrapper. This allows
// developers of custom FeatureStore implementations to avoid repeating logic that would
// commonly be needed in any such implementation, such as caching. Instead, they can
// implement only FeatureStoreCore and then call NewFeatureStoreWrapper.
//
// This interface assumes that the feature store can update the data set atomically. If
// not, use NonAtomicFeatureStoreCore instead. FeatureStoreCoreBase defines the common methods.
type FeatureStoreCore interface {
	FeatureStoreCoreBase
	// InitInternal replaces the entire contents of the data store. This should be done
	// atomically (i.e. within a transaction).
	InitInternal(map[ld.VersionedDataKind]map[string]ld.VersionedData) error
}

// NonAtomicFeatureStoreCore is an interface for a limited subset of the functionality of
// ldclient.FeatureStore, to be used in conjunction with FeatureStoreWrapper. This allows
// developers of custom FeatureStore implementations to avoid repeating logic that would
// commonly be needed in any such implementation, such as caching. Instead, they can
// implement only FeatureStoreCore and then call NewFeatureStoreWrapper.
//
// This interface assumes that the feature store cannot update the data set atomically and
// will require the SDK to specify the order of operations. If atomic updates are possible,
// then use FeatureStoreCore instead. FeatureStoreCoreBase defines the common methods.
//
// Note that this is somewhat different from the way the LaunchDarkly SDK addresses the
// atomicity issue on most other platforms. There, the feature stores just have one
// interface, which always receives the data as a map, but the SDK can control the
// iteration order of the map. That isn't possible in Go where maps never have a defined
// iteration order.
type NonAtomicFeatureStoreCore interface {
	FeatureStoreCoreBase
	// InitCollectionsInternal replaces the entire contents of the data store. The SDK will
	// pass a data set with a defined ordering; the collections (kinds) should be processed in
	// the specified order, and the items within each collection should be written in the
	// specified order. The store should delete any obsolete items only after writing all of
	// the items provided.
	InitCollectionsInternal(allData []StoreCollection) error
}

// StoreCollection is used by the NonAtomicFeatureStoreCore interface.
type StoreCollection struct {
	Kind  ld.VersionedDataKind
	Items []ld.VersionedData
}

// FeatureStoreWrapper is a partial implementation of ldclient.FeatureStore that delegates basic
// functionality to an instance of FeatureStoreCore. It provides optional caching, and will
// automatically provide the proper data ordering when using  NonAtomicFeatureStoreCoreInitialization.
type FeatureStoreWrapper struct {
	core          FeatureStoreCoreBase
	coreAtomic    FeatureStoreCore
	coreNonAtomic NonAtomicFeatureStoreCore
	cache         *cache.Cache
	inited        bool
	initLock      sync.RWMutex
}

const initCheckedKey = "$initChecked"

// NewFeatureStoreWrapper creates an instance of FeatureStoreWrapper that wraps an instance
// of FeatureStoreCore.
func NewFeatureStoreWrapper(core FeatureStoreCore) *FeatureStoreWrapper {
	w := FeatureStoreWrapper{core: core, coreAtomic: core}
	w.cache = initCache(core)
	return &w
}

// NewNonAtomicFeatureStoreWrapper creates an instance of FeatureStoreWrapper that wraps an
// instance of NonAtomicFeatureStoreCore.
func NewNonAtomicFeatureStoreWrapper(core NonAtomicFeatureStoreCore) *FeatureStoreWrapper {
	w := FeatureStoreWrapper{core: core, coreNonAtomic: core}
	w.cache = initCache(core)
	return &w
}

func initCache(core FeatureStoreCoreBase) *cache.Cache {
	cacheTTL := core.GetCacheTTL()
	if cacheTTL > 0 {
		return cache.New(cacheTTL, 5*time.Minute)
	}
	return nil
}

func featureStoreCacheKey(kind ld.VersionedDataKind, key string) string {
	return kind.GetNamespace() + ":" + key
}

func featureStoreAllItemsCacheKey(kind ld.VersionedDataKind) string {
	return "all:" + kind.GetNamespace()
}

// Init performs an update of the entire data store, with optional caching.
func (w *FeatureStoreWrapper) Init(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	var err error
	if w.coreNonAtomic != nil {
		// If the store uses non-atomic initialization, we'll need to put the data in the proper update
		// order and call InitCollectionsInternal.
		colls := transformUnorderedDataToOrderedData(allData)
		err = w.coreNonAtomic.InitCollectionsInternal(colls)
	} else {
		err = w.coreAtomic.InitInternal(allData)
	}
	if w.cache != nil {
		w.cache.Flush()
		if err == nil {
			for kind, items := range allData {
				w.filterAndCacheItems(kind, items)
			}
		}
	}
	if err == nil {
		w.initLock.Lock()
		defer w.initLock.Unlock()
		w.inited = true
	}
	return err
}

func (w *FeatureStoreWrapper) filterAndCacheItems(kind ld.VersionedDataKind, items map[string]ld.VersionedData) map[string]ld.VersionedData {
	// We do some filtering here so that deleted items are not included in the full cached data set
	// that's used by All. This is so that All doesn't have to do that filtering itself. However,
	// since Get does know to filter out deleted items, we will still cache those individually,
	filteredItems := make(map[string]ld.VersionedData, len(items))
	for key, item := range items {
		if !item.IsDeleted() {
			filteredItems[key] = item
		}
		if w.cache != nil {
			w.cache.Set(featureStoreCacheKey(kind, key), item, cache.DefaultExpiration)
		}
	}
	if w.cache != nil {
		w.cache.Set(featureStoreAllItemsCacheKey(kind), filteredItems, cache.DefaultExpiration)
	}
	return filteredItems
}

// Get retrieves a single item by key, with optional caching.
func (w *FeatureStoreWrapper) Get(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	if w.cache == nil {
		item, err := w.core.GetInternal(kind, key)
		return itemOnlyIfNotDeleted(item), err
	}
	cacheKey := featureStoreCacheKey(kind, key)
	if data, present := w.cache.Get(cacheKey); present {
		if data == nil { // If present is true but data is nil, we have cached the absence of an item
			return nil, nil
		}
		if item, ok := data.(ld.VersionedData); ok {
			return itemOnlyIfNotDeleted(item), nil
		}
	}
	// Item was not cached or cached value was not valid
	item, err := w.core.GetInternal(kind, key)
	if err == nil {
		w.cache.Set(cacheKey, item, cache.DefaultExpiration)
	}
	return itemOnlyIfNotDeleted(item), err
}

func itemOnlyIfNotDeleted(item ld.VersionedData) ld.VersionedData {
	if item != nil && item.IsDeleted() {
		return nil
	}
	return item
}

// All retrieves all items of the specified kind, with optional caching.
func (w *FeatureStoreWrapper) All(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {
	if w.cache == nil {
		return w.core.GetAllInternal(kind)
	}
	// Check whether we have a cache item for the entire data set
	cacheKey := featureStoreAllItemsCacheKey(kind)
	if data, present := w.cache.Get(cacheKey); present {
		if items, ok := data.(map[string]ld.VersionedData); ok {
			return items, nil
		}
	}
	// Data set was not cached or cached value was not valid
	items, err := w.core.GetAllInternal(kind)
	if err != nil {
		return nil, err
	}
	return w.filterAndCacheItems(kind, items), nil
}

// Upsert updates or adds an item, with optional caching.
func (w *FeatureStoreWrapper) Upsert(kind ld.VersionedDataKind, item ld.VersionedData) error {
	finalItem, err := w.core.UpsertInternal(kind, item)
	// Note that what we put into the cache is finalItem, which may not be the same as item (i.e. if
	// another process has already updated the item to a higher version).
	if err == nil && finalItem != nil {
		if w.cache != nil {
			w.cache.Set(featureStoreCacheKey(kind, item.GetKey()), finalItem, cache.DefaultExpiration)
			w.cache.Delete(featureStoreAllItemsCacheKey(kind))
		}
	}
	return err
}

// Delete deletes an item, with optional caching.
func (w *FeatureStoreWrapper) Delete(kind ld.VersionedDataKind, key string, version int) error {
	deletedItem := kind.MakeDeletedItem(key, version)
	return w.Upsert(kind, deletedItem)
}

// Initialized returns true if the feature store contains a data set. To avoid calling the
// underlying implementation any more often than necessary (since Initialized is called often),
// FeatureStoreWrapper uses the following heuristic: 1. Once we have received a true result
// from InitializedInternal, we always return true. 2. If InitializedInternal returns false,
// and we have a cache, we will cache that result so we won't call it any more frequently
// than the cache TTL.
func (w *FeatureStoreWrapper) Initialized() bool {
	w.initLock.RLock()
	previousValue := w.inited
	w.initLock.RUnlock()
	if previousValue {
		return true
	}

	if w.cache != nil {
		if _, found := w.cache.Get(initCheckedKey); found {
			return false
		}
	}

	newValue := w.core.InitializedInternal()
	if newValue {
		w.initLock.Lock()
		defer w.initLock.Unlock()
		w.inited = true
		if w.cache != nil {
			w.cache.Delete(initCheckedKey)
		}
	} else {
		if w.cache != nil {
			w.cache.Set(initCheckedKey, "", cache.DefaultExpiration)
		}
	}
	return newValue
}
