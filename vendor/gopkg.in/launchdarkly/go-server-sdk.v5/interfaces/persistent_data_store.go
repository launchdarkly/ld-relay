package interfaces

import "io"

// PersistentDataStore is an interface for a data store that holds feature flags and related data in a
// serialized form.
//
// This interface should be used for database integrations, or any other data store implementation that
// stores data in some external service. The SDK will provide its own caching layer on top of the
// persistent data store; the data store implementation should not provide caching, but simply do every
// query or update that the SDK tells it to do.
//
// Implementations must be safe for concurrent access from multiple goroutines.
//
// Error handling is defined as follows: if any data store operation encounters a database error, or
// is otherwise unable to complete its task, it should return an error value to make the SDK aware of
// The SDK will log the exception and will assume that the data store is now in a non-operational
// non-operational state; the SDK will then start polling IsStoreAvailable() to determine when the
// store has started working again.
type PersistentDataStore interface {
	io.Closer

	// Init overwrites the store's contents with a set of items for each collection.
	//
	// All previous data should be discarded, regardless of versioning.
	//
	// The update should be done atomically. If it cannot be done atomically, then the store
	// must first add or update each item in the same order that they are given in the input
	// data, and then delete any previously stored items that were not in the input data.
	Init(allData []StoreSerializedCollection) error

	// Get retrieves an item from the specified collection, if available.
	//
	// If the specified key does not exist in the collection, it should return a StoreSerializedItemDescriptor
	// with a Version of -1 and an Item of nil.
	//
	// If the item has been deleted and the store contains a placeholder, it should return that
	// placeholder rather than filtering it out.
	Get(kind StoreDataKind, key string) (StoreSerializedItemDescriptor, error)

	// GetAll retrieves all items from the specified collection.
	//
	// If the store contains placeholders for deleted items, it should include them in the results,
	// not filter them out.
	GetAll(kind StoreDataKind) ([]StoreKeyedSerializedItemDescriptor, error)

	// Upsert updates or inserts an item in the specified collection. For updates, the object will only be
	// updated if the existing version is less than the new version.
	//
	// The SDK may pass StoreSerializedItemDescriptor that represents a placeholder for a deleted item. In
	// that case, assuming the version is greater than any existing version of that item, the store should
	// retain that placeholder rather than simply not storing anything.
	//
	// The method returns the updated item if the update was successful; or, if the update was not
	// successful because the store contained an equal or higher version, it returns the item that is
	// in the store.
	Upsert(kind StoreDataKind, key string, item StoreSerializedItemDescriptor) (bool, error)

	// IsInitialized returns true if the data store contains a data set, meaning that Init has been
	// called at least once.
	//
	// In a shared data store, it should be able to detect this even if Init was called in a
	// different process: that is, the test should be based on looking at what is in the data store.
	// Once this has been determined to be true, it can continue to return true without having to
	// check the store again; this method should be as fast as possible since it may be called during
	// feature flag evaluations.
	IsInitialized() bool

	// IsStoreAvailable tests whether the data store seems to be functioning normally.
	//
	// This should not be a detailed test of different kinds of operations, but just the smallest possible
	// operation to determine whether (for instance) we can reach the database.
	//
	// Whenever one of the store's other methods returns an error, the SDK will assume that it may have
	// become unavailable (e.g. the database connection was lost). The SDK will then call
	// IsStoreAvailable() at intervals until it returns true.
	IsStoreAvailable() bool
}

// PersistentDataStoreFactory is an interface for a factory that creates some implementation of a
// persistent data store.
//
// This interface is implemented by database integrations. Usage is described in
// ldcomponents.PersistentDataStore().
type PersistentDataStoreFactory interface {
	// CreateDataStore is called by the SDK to create the implementation instance.
	//
	// This happens only when MakeClient or MakeCustomClient is called. The implementation instance
	// is then tied to the life cycle of the LDClient, so it will receive a Close() call when the
	// client is closed.
	//
	// If the factory returns an error, creation of the LDClient fails.
	CreatePersistentDataStore(context ClientContext) (PersistentDataStore, error)
}
