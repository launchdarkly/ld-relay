package interfaces

// DataStoreStatusProvider is an interface for querying the status of a persistent data store.
//
// An implementation of this interface is returned by LDClient.GetDataStoreStatusProvider(). Application code
// should not implement this interface.
type DataStoreStatusProvider interface {
	// GetStatus returns the current status of the store.
	//
	// This is only meaningful for persistent stores, or any other DataStore implementation that makes use of
	// the reporting mechanism that is provided by DataStoreUpdates. For the default in-memory store, the
	// status will always be reported as "available".
	GetStatus() DataStoreStatus

	// Indicates whether the current data store implementation supports status monitoring.
	//
	// This is normally true for all persistent data stores, and false for the default in-memory store. A true
	// value means that any listeners added with AddStatusListener() can expect to be notified if there is
	// there is any error in storing data, and then notified again when the error condition is resolved. A
	// false value means that the status is not meaningful and listeners should not expect to be notified.
	IsStatusMonitoringEnabled() bool

	// AddStatusListener subscribes for notifications of status changes. The returned channel will receive a
	// new DataStoreStatus value for any change in status.
	//
	// Applications may wish to know if there is an outage in a persistent data store, since that could mean
	// that flag evaluations are unable to get the flag data from the store (unless it is currently cached) and
	// therefore might return default values.
	//
	// If the SDK receives an exception while trying to query or update the data store, then it publishes a
	// DataStoreStatus where Available is false, to indicate that the store appears to be offline, and begins
	// polling the store at intervals until a query succeeds. Once it succeeds, it publishes a new status where
	// Available is true.
	//
	// If the data store implementation does not support status tracking, such as if you are using the default
	// in-memory store rather than a persistent store, it will return a channel that never receives values.
	//
	// It is the caller's responsibility to consume values from the channel. Allowing values to accumulate in
	// the channel can cause an SDK goroutine to be blocked.
	AddStatusListener() <-chan DataStoreStatus

	// RemoveStatusListener unsubscribes from notifications of status changes. The specified channel must be
	// one that was previously returned by AddStatusListener(); otherwise, the method has no effect.
	RemoveStatusListener(<-chan DataStoreStatus)
}

// DataStoreStatus contains information about the status of a data store, provided by DataStoreStatusProvider.
type DataStoreStatus struct {
	// Available is true if the SDK believes the data store is now available.
	//
	// This property is normally true. If the SDK receives an exception while trying to query or update the
	// data store, then it sets this property to false (notifying listeners, if any) and polls the store at
	// intervals until a query succeeds. Once it succeeds, it sets the property back to true (again
	// notifying listeners).
	Available bool

	// NeedsRefresh is true if the store may be out of date due to a previous outage, so the SDK should
	// attempt to refresh all feature flag data and rewrite it to the store.
	//
	// This property is not meaningful to application code.
	NeedsRefresh bool
}
