package ldclient

// Common interface for string-keyed, versioned objects such as feature flags.
type VersionedData interface {
	// Return the string key for this object.
	GetKey() string
	// Return the version number for this object.
	GetVersion() int
	// Return whether or not this object has been deleted.
	IsDeleted() bool
}

// Data structure that describes a kind of VersionedData objects that may exist in a store.
type VersionedDataKind interface {
	// Return a short string that serves as the unique name for the collection of these objects, e.g. "features".
	GetNamespace() string
	// Return a pointer to a newly created null value of this object type. This is used for JSON unmarshalling.
	GetDefaultItem() interface{}
	// Return a value of this object type with the specified key and version, and Deleted=true.
	MakeDeletedItem(key string, version int) VersionedData
}
