package autoconfigcache

import (
	"encoding/json"
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
)

// ModelKind identifies the type of data stored in a CachedItem.
type ModelKind string

const (
	ModelKindEnvironment ModelKind = "environment"
	ModelKindFilter      ModelKind = "filter"
)

// CurrentModelVersion is the version of the serialization format.
// Increment this when the shape of EnvironmentRep or FilterRep changes.
const CurrentModelVersion = 2

// CachedItem is the versioned envelope stored in the cache. It wraps the actual data
// with kind and version metadata so we can detect and handle format changes on read.
type CachedItem struct {
	Kind         ModelKind       `json:"kind"`
	ModelVersion int             `json:"modelVersion"`
	Data         json.RawMessage `json:"data"`
}

// marshalCachedItem wraps data in a versioned envelope and returns the JSON bytes.
func marshalCachedItem(kind ModelKind, data interface{}) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(CachedItem{
		Kind:         kind,
		ModelVersion: CurrentModelVersion,
		Data:         raw,
	})
}

// unmarshalCachedItem decodes the envelope. If the model version is not recognized,
// it returns an error so the caller can skip the item gracefully.
func unmarshalCachedItem(raw []byte) (CachedItem, error) {
	var item CachedItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return CachedItem{}, fmt.Errorf("invalid cached item envelope: %w", err)
	}
	if item.ModelVersion != CurrentModelVersion {
		return CachedItem{}, fmt.Errorf("unsupported model version %d (expected %d)", item.ModelVersion, CurrentModelVersion)
	}
	return item, nil
}

// modelKindFromCacheKind converts the autoconfig.CacheKind used by the stream layer
// into the ModelKind used for serialization.
func modelKindFromCacheKind(kind autoconfig.CacheKind) ModelKind {
	switch kind {
	case autoconfig.CacheKindEnvironment:
		return ModelKindEnvironment
	case autoconfig.CacheKindFilter:
		return ModelKindFilter
	default:
		return ModelKind(fmt.Sprintf("unknown-%d", kind))
	}
}
