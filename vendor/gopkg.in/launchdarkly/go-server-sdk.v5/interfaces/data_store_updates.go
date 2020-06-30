package interfaces

// DataStoreUpdates is an interface that a data store implementation can use to report information
// back to the SDK.
//
// Application code does not need to use this type. It is for data store implementations.
//
// The DataStoreFactory receives an implementation of this interface and can pass it to the data
// store that it creates, if desired.
type DataStoreUpdates interface {
	// UpdateStatus informs the SDK of a change in the data store's operational status.
	//
	// This is what makes the status monitoring mechanisms in DataStoreStatusProvider work.
	UpdateStatus(newStatus DataStoreStatus)
}
