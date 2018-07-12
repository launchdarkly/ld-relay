package ldclient

// VersionedData is a common interface for string-keyed, versioned objects such as feature flags.
type VersionedData interface {
	// GetKey returns the string key for this object.
	GetKey() string
	// GetVersion returns the version number for this object.
	GetVersion() int
	// IsDeleted returns whether or not this object has been deleted.
	IsDeleted() bool
}

// VersionedDataKind describes a kind of VersionedData objects that may exist in a store.
type VersionedDataKind interface {
	// GetNamespace returns a short string that serves as the unique name for the collection of these objects, e.g. "features".
	GetNamespace() string
	// GetDefaultItem return a pointer to a newly created null value of this object type. This is used for JSON unmarshalling.
	GetDefaultItem() interface{}
	// MakeDeletedItem returns a value of this object type with the specified key and version, and Deleted=true.
	MakeDeletedItem(key string, version int) VersionedData
}

// VersionedDataKinds is a list of supported VersionedDataKind's. Among other things, this list might
// be used by feature stores to know what data (namespaces) to expect.
var VersionedDataKinds = [...]VersionedDataKind{
	Features,
	Segments,
}
