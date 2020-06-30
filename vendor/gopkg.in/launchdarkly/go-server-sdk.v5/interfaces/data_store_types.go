package interfaces

// StoreDataKind represents a separately namespaced collection of storable data items.
//
// Application code does not need to use this type. It is for data store implementations.
//
// The SDK passes instances of this type to the data store to specify whether it is referring to
// a feature flag, a user segment, etc. The data store implementation should not look for a
// specific data kind (such as feature flags), but should treat all data kinds generically.
type StoreDataKind interface {
	GetName() string
	Serialize(item StoreItemDescriptor) []byte
	Deserialize(data []byte) (StoreItemDescriptor, error)
}

// StoreItemDescriptor is a versioned item (or placeholder) storable in a DataStore.
//
// Application code does not need to use this type. It is for data store implementations.
//
// This is used for data stores that directly store objects as-is, as the default in-memory
// store does. Items are typed as interface{}; the store should not know or care what the
// actual object is.
//
// For any given key within a StoreDataKind, there can be either an existing item with a
// version, or a "tombstone" placeholder representing a deleted item (also with a version).
// Deleted item placeholders are used so that if an item is first updated with version N and
// then deleted with version N+1, but the SDK receives those changes out of order, version N
// will not overwrite the deletion.
//
// Persistent data stores use StoreSerializedItemDescriptor instead.
type StoreItemDescriptor struct {
	// Version is the version number of this data, provided by the SDK.
	Version int
	// Item is the data item, or nil if this is a placeholder for a deleted item.
	Item interface{}
}

// NotFound is a convenience method to return a value indicating no such item exists.
func (s StoreItemDescriptor) NotFound() StoreItemDescriptor {
	return StoreItemDescriptor{Version: -1, Item: nil}
}

// StoreSerializedItemDescriptor is a versioned item (or placeholder) storable in a PersistentDataStore.
//
// Application code does not need to use this type. It is for data store implementations.
//
// This is equivalent to StoreItemDescriptor, but is used for persistent data stores. The
// SDK will convert each data item to and from its serialized string form; the persistent data
// store deals only with the serialized form.
type StoreSerializedItemDescriptor struct {
	// Version is the version number of this data, provided by the SDK.
	Version int
	// Deleted is true if this is a placeholder (tombstone) for a deleted item. If so,
	// SerializedItem will still contain a byte string representing the deleted item, but
	// the persistent store implementation has the option of not storing it if it can represent the
	// placeholder in a more efficient way.
	Deleted bool
	// SerializedItem is the data item's serialized representation. For a deleted item placeholder,
	// instead of passing nil, the SDK will provide a special value that can be stored if necessary
	// (see Deleted).
	SerializedItem []byte
}

// NotFound is a convenience method to return a value indicating no such item exists.
func (s StoreSerializedItemDescriptor) NotFound() StoreSerializedItemDescriptor {
	return StoreSerializedItemDescriptor{Version: -1, SerializedItem: nil}
}

// StoreKeyedItemDescriptor is a key-value pair containing a StoreItemDescriptor.
//
// Application code does not need to use this type. It is for data store implementations.
type StoreKeyedItemDescriptor struct {
	// Key is the unique key of this item within its StoreDataKind.
	Key string
	// Item is the versioned item.
	Item StoreItemDescriptor
}

// StoreKeyedSerializedItemDescriptor is a key-value pair containing a StoreSerializedItemDescriptor.
//
// Application code does not need to use this type. It is for data store implementations.
type StoreKeyedSerializedItemDescriptor struct {
	// Key is the unique key of this item within its StoreDataKind.
	Key string
	// Item is the versioned serialized item.
	Item StoreSerializedItemDescriptor
}

// StoreCollection is a list of data store items for a StoreDataKind.
//
// Application code does not need to use this type. It is for data store implementations.
type StoreCollection struct {
	Kind  StoreDataKind
	Items []StoreKeyedItemDescriptor
}

// StoreSerializedCollection is a list of serialized data store items for a StoreDataKind.
//
// Application code does not need to use this type. It is for data store implementations.
type StoreSerializedCollection struct {
	Kind  StoreDataKind
	Items []StoreKeyedSerializedItemDescriptor
}
