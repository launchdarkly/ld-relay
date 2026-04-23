package autoconfigcache

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalUnmarshalRoundtrip(t *testing.T) {
	type testData struct {
		Name string `json:"name"`
	}

	raw, err := marshalCachedItem(ModelKindEnvironment, testData{Name: "test-env"})
	require.NoError(t, err)

	item, err := unmarshalCachedItem(raw)
	require.NoError(t, err)

	assert.Equal(t, ModelKindEnvironment, item.Kind)
	assert.Equal(t, CurrentModelVersion, item.ModelVersion)

	var data testData
	require.NoError(t, json.Unmarshal(item.Data, &data))
	assert.Equal(t, "test-env", data.Name)
}

func TestUnmarshalRejectsUnknownVersion(t *testing.T) {
	raw, _ := json.Marshal(CachedItem{
		Kind:         ModelKindEnvironment,
		ModelVersion: 999,
		Data:         json.RawMessage(`{}`),
	})

	_, err := unmarshalCachedItem(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported model version")
}

func TestUnmarshalRejectsInvalidJSON(t *testing.T) {
	_, err := unmarshalCachedItem([]byte("not json"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cached item envelope")
}

func TestModelKindFromCacheKind(t *testing.T) {
	assert.Equal(t, ModelKindEnvironment, modelKindFromCacheKind(autoconfig.CacheKindEnvironment))
	assert.Equal(t, ModelKindFilter, modelKindFromCacheKind(autoconfig.CacheKindFilter))
}
