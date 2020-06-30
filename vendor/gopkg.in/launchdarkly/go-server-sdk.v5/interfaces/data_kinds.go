package interfaces

import (
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

//nolint:gochecknoglobals // global used as a constant for efficiency
var modelSerialization = ldmodel.NewJSONDataModelSerialization()

// StoreDataKinds returns a list of supported StoreDataKinds. Among other things, this list might
// be used by data stores to know what data (namespaces) to expect.
func StoreDataKinds() []StoreDataKind {
	return []StoreDataKind{dataKindFeatures, dataKindSegments}
}

// featureFlagStoreDataKind implements StoreDataKind
type featureFlagStoreDataKind struct{}

// GetName returns the unique namespace identifier for feature flag objects.
func (fk featureFlagStoreDataKind) GetName() string {
	return "features"
}

// Serialize is used internally by the SDK when communicating with a PersistentDataStore.
func (fk featureFlagStoreDataKind) Serialize(item StoreItemDescriptor) []byte {
	if flag, ok := item.Item.(*ldmodel.FeatureFlag); ok {
		if bytes, err := modelSerialization.MarshalFeatureFlag(*flag); err == nil {
			return bytes
		}
	}
	return nil
}

// Deserialize is used internally by the SDK when communicating with a PersistentDataStore.
func (fk featureFlagStoreDataKind) Deserialize(data []byte) (StoreItemDescriptor, error) {
	flag, err := modelSerialization.UnmarshalFeatureFlag(data)
	if err != nil {
		return StoreItemDescriptor{}, err
	}
	if flag.Deleted {
		return StoreItemDescriptor{flag.Version, nil}, nil
	}
	return StoreItemDescriptor{flag.Version, &flag}, nil
}

// String returns a human-readable string identifier.
func (fk featureFlagStoreDataKind) String() string {
	return fk.GetName()
}

//nolint:gochecknoglobals // global used as a constant for efficiency
var dataKindFeatures StoreDataKind = featureFlagStoreDataKind{}

// DataKindFeatures returns the StoreDataKind instance corresponding to feature flag data.
func DataKindFeatures() StoreDataKind {
	return dataKindFeatures
}

// segmentStoreDataKind implements StoreDataKind and provides methods to build storage engine for segments.
type segmentStoreDataKind struct{}

// GetName returns the unique namespace identifier for segment objects.
func (sk segmentStoreDataKind) GetName() string {
	return "segments"
}

// Serialize is used internally by the SDK when communicating with a PersistentDataStore.
func (sk segmentStoreDataKind) Serialize(item StoreItemDescriptor) []byte {
	if segment, ok := item.Item.(*ldmodel.Segment); ok {
		if bytes, err := modelSerialization.MarshalSegment(*segment); err == nil {
			return bytes
		}
	}
	return nil
}

// Deserialize is used internally by the SDK when communicating with a PersistentDataStore.
func (sk segmentStoreDataKind) Deserialize(data []byte) (StoreItemDescriptor, error) {
	segment, err := modelSerialization.UnmarshalSegment(data)
	if err != nil {
		return StoreItemDescriptor{}, err
	}
	if segment.Deleted {
		return StoreItemDescriptor{segment.Version, nil}, nil
	}
	return StoreItemDescriptor{segment.Version, &segment}, nil
}

// String returns a human-readable string identifier.
func (sk segmentStoreDataKind) String() string {
	return sk.GetName()
}

//nolint:gochecknoglobals // global used as a constant for efficiency
var dataKindSegments StoreDataKind = segmentStoreDataKind{}

// DataKindSegments returns the StoreDataKind instance corresponding to user segment data.
func DataKindSegments() StoreDataKind {
	return dataKindSegments
}
