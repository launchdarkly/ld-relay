package interfaces

// DataSourceUpdates is an interface that a data source implementation will use to push data into the SDK.
//
// Application code does not need to use this type. It is for data source implementations.
//
// The data source interacts with this object, rather than manipulating the data store directly, so that
// the SDK can perform any other necessary operations that must happen when data is updated.
type DataSourceUpdates interface {
	// Init overwrites the current contents of the data store with a set of items for each collection.
	//
	// If the underlying data store returns an error during this operation, the SDK will log it,
	// and set the data source state to DataSourceStateInterrupted with an error of
	// DataSourceErrorKindStoreError. It will not return the error to the data source, but will
	// return false to indicate that the operation failed.
	Init(allData []StoreCollection) bool

	// Upsert updates or inserts an item in the specified collection. For updates, the object will only be
	// updated if the existing version is less than the new version.
	//
	// To mark an item as deleted, pass a StoreItemDescriptor with a nil Item and a nonzero version
	// number. Deletions must be versioned so that they do not overwrite a later update in case updates
	// are received out of order.
	//
	// If the underlying data store returns an error during this operation, the SDK will log it,
	// and set the data source state to DataSourceStateInterrupted with an error of
	// DataSourceErrorKindStoreError. It will not return the error to the data source, but will
	// return false to indicate that the operation failed.
	Upsert(kind StoreDataKind, key string, item StoreItemDescriptor) bool

	// UpdateStatus informs the SDK of a change in the data source's status.
	//
	// Data source implementations should use this method if they have any concept of being in a valid
	// state, a temporarily disconnected state, or a permanently stopped state.
	//
	// If newState is different from the previous state, and/or newError is non-empty, the SDK
	// will start returning the new status (adding a timestamp for the change) from
	// DataSourceStatusProvider.GetStatus(), and will trigger status change events to any
	// registered listeners.
	//
	// A special case is that if newState is DataSourceStateInterrupted, but the previous state was
	// but the previous state was DataSourceStateInitializing, the state will remain at Initializing
	// because Interrupted is only meaningful after a successful startup.
	UpdateStatus(newState DataSourceState, newError DataSourceErrorInfo)

	// GetDataStoreStatusProvider returns an object that provides status tracking for the data store, if
	// applicable.
	//
	// This may be useful if the data source needs to be aware of storage problems that might require it
	// to take some special action: for instance, if a database outage may have caused some data to be
	// lost and therefore the data should be re-requested from LaunchDarkly.
	GetDataStoreStatusProvider() DataStoreStatusProvider
}
