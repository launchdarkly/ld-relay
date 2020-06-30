package interfaces

import "io"

// DataStoreFactory is a factory that creates some implementation of DataStore.
type DataStoreFactory interface {
	// CreateDataStore is called by the SDK to create the implementation instance.
	//
	// This happens only when MakeClient or MakeCustomClient is called. The implementation instance
	// is then tied to the life cycle of the LDClient, so it will receive a Close() call when the
	// client is closed.
	//
	// If the factory returns an error, creation of the LDClient fails.
	CreateDataStore(context ClientContext, dataStoreUpdates DataStoreUpdates) (DataStore, error)
}

// DataStore is an interface for a data store that holds feature flags and related data received by
// the SDK.
//
// Ordinarily, the only implementations of this interface are the default in-memory implementation,
// which holds references to actual SDK data model objects, and the persistent data store
// implementation that delegates to a PersistentDataStore.
type DataStore interface {
	io.Closer

	// Init overwrites the store's contents with a set of items for each collection.
	//
	// All previous data should be discarded, regardless of versioning.
	//
	// The update should be done atomically. If it cannot be done atomically, then the store
	// must first add or update each item in the same order that they are given in the input
	// data, and then delete any previously stored items that were not in the input data.
	Init(allData []StoreCollection) error

	// Get retrieves an item from the specified collection, if available.
	//
	// If the specified key does not exist in the collection, it should return a StoreItemDescriptor
	// whose Version is -1.
	//
	// If the item has been deleted and the store contains a placeholder, it should return a
	// StoreItemDescriptor whose Version is the version of the placeholder, and whose Item is nil.
	Get(kind StoreDataKind, key string) (StoreItemDescriptor, error)

	// GetAll retrieves all items from the specified collection.
	//
	// If the store contains placeholders for deleted items, it should include them in the results,
	// not filter them out.
	GetAll(kind StoreDataKind) ([]StoreKeyedItemDescriptor, error)

	// Upsert updates or inserts an item in the specified collection. For updates, the object will only be
	// updated if the existing version is less than the new version.
	//
	// The SDK may pass an ItemDescriptor whose Item is nil, to represent a placeholder for a deleted
	// item. In that case, assuming the version is greater than any existing version of that item, the
	// store should retain that placeholder rather than simply not storing anything.
	//
	// The method returns true if the item was updated, or false if it was not updated because the store
	// contains an equal or greater version.
	Upsert(kind StoreDataKind, key string, item StoreItemDescriptor) (bool, error)

	// IsInitialized returns true if the data store contains a data set, meaning that Init has been
	// called at least once.
	//
	// In a shared data store, it should be able to detect this even if Init was called in a
	// different process: that is, the test should be based on looking at what is in the data store.
	// Once this has been determined to be true, it can continue to return true without having to
	// check the store again; this method should be as fast as possible since it may be called during
	// feature flag evaluations.
	IsInitialized() bool

	// IsStatusMonitoringEnabled returns true if this data store implementation supports status
	// monitoring.
	//
	// This is normally only true for persistent data stores created with ldcomponents.PersistentDataStore(),
	// but it could also be true for any custom {@link DataStore} implementation that makes use of the
	// statusUpdater parameter provided to the DataStoreFactory. Returning true means that the store
	// guarantees that if it ever enters an invalid state (that is, an operation has failed or it knows
	// that operations cannot succeed at the moment), it will publish a status update, and will then
	// publish another status update once it has returned to a valid state.
	//
	// The same value will be returned from DataStoreStatusProvider.IsStatusMonitoringEnabled().
	IsStatusMonitoringEnabled() bool
}
