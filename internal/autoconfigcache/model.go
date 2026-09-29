package autoconfigcache

import (
	"encoding/json"
	"fmt"

	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
)

// ModelKind identifies the type of data stored in a CachedItem.
type ModelKind string

const (
	ModelKindEnvironment ModelKind = "environment"
	// ModelKindFilter is obsolete. Payload filters are not supported, so nothing writes this kind
	// any more, but a store written by an earlier version still holds rows marked with it. The
	// constant stays so those rows are recognized and skipped quietly, instead of being reported as
	// an unknown kind on every read.
	ModelKindFilter ModelKind = "filter"
)

// CurrentModelVersion is the version of the serialization format.
// Increment this when the shape of EnvironmentRep changes.
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
//
// Any version up to the current one is readable: EnvironmentRep only ever gains fields, and ToParams
// synthesizes the key arrays from sdkKey and mobKey when an older entry omits them. Refusing a lower
// version would discard the whole cache on the first start after an upgrade, which is the outage the
// cache exists to prevent. The check stays one-sided so an older relay still refuses an entry
// written by a newer one, whose shape it cannot know.
func unmarshalCachedItem(raw []byte) (CachedItem, error) {
	var item CachedItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return CachedItem{}, fmt.Errorf("invalid cached item envelope: %w", err)
	}
	if item.ModelVersion < 1 || item.ModelVersion > CurrentModelVersion {
		return CachedItem{}, fmt.Errorf("unsupported model version %d (expected 1 to %d)", item.ModelVersion, CurrentModelVersion)
	}
	return item, nil
}

// modelKindFromCacheKind converts the autoconfig.CacheKind used by the stream layer
// into the ModelKind used for serialization.
func modelKindFromCacheKind(kind autoconfig.CacheKind) ModelKind {
	switch kind {
	case autoconfig.CacheKindEnvironment:
		return ModelKindEnvironment
	default:
		return ModelKind(fmt.Sprintf("unknown-%d", kind))
	}
}
