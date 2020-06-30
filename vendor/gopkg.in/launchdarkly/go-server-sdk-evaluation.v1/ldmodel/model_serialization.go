package ldmodel

import (
	"encoding/json"
)

// DataModelSerialization defines an encoding for SDK data model objects.
//
// For the default JSON encoding used by LaunchDarkly SDKs, use NewJSONDataModelSerialization.
type DataModelSerialization interface {
	// MarshalFeatureFlag converts a FeatureFlag into its serialized encoding.
	MarshalFeatureFlag(item FeatureFlag) ([]byte, error)
	// MarshalSegment converts a Segment into its serialized encoding.
	MarshalSegment(item Segment) ([]byte, error)
	// UnmarshalFeatureFlag attempts to convert a FeatureFlag from its serialized encoding.
	UnmarshalFeatureFlag(data []byte) (FeatureFlag, error)
	// UnmarshalFeatureFlag attempts to convert a FeatureFlag from its serialized encoding.
	UnmarshalSegment(data []byte) (Segment, error)
}

type jsonDataModelSerialization struct{}

// NewJSONDataModelSerialization provides the default JSON encoding for SDK data model objects.
//
// Always use this rather than relying on json.Marshal() and json.Unmarshal(). The data model
// structs are guaranteed to serialize and deserialize correctly with json.Marshal() and
// json.Unmarshal(), but JSONDataModelSerialization may be enhanced in the future to use a
// more efficient mechanism.
func NewJSONDataModelSerialization() DataModelSerialization {
	return jsonDataModelSerialization{}
}

func (s jsonDataModelSerialization) MarshalFeatureFlag(item FeatureFlag) ([]byte, error) {
	return json.Marshal(item)
}

func (s jsonDataModelSerialization) MarshalSegment(item Segment) ([]byte, error) {
	return json.Marshal(item)
}

func (s jsonDataModelSerialization) UnmarshalFeatureFlag(data []byte) (FeatureFlag, error) {
	var item FeatureFlag
	err := json.Unmarshal(data, &item)
	if err == nil {
		PreprocessFlag(&item)
	}
	return item, err
}

func (s jsonDataModelSerialization) UnmarshalSegment(data []byte) (Segment, error) {
	var item Segment
	err := json.Unmarshal(data, &item)
	if err == nil {
		PreprocessSegment(&item)
	}
	return item, err
}
