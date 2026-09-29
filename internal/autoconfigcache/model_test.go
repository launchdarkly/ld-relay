package autoconfigcache

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
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

func TestUnmarshalAcceptsObsoleteFilterKind(t *testing.T) {
	// A store written by a version that supported payload filters still holds rows marked as the
	// filter kind. Reading one must not fail: the envelope has to accept it so the read paths in
	// redisStore and dynamoDBStore can recognize the kind and skip it. If the envelope rejected it
	// instead, the first read after an upgrade would error rather than skip.
	raw, err := marshalCachedItem(ModelKindFilter, map[string]string{"key": "microservice-a"})
	require.NoError(t, err)

	item, err := unmarshalCachedItem(raw)
	require.NoError(t, err, "a persisted filter row must still unmarshal")
	assert.Equal(t, ModelKindFilter, item.Kind)
}

func TestUnmarshalReadsAnEntryFromAnEarlierModelVersion(t *testing.T) {
	// The first start after an upgrade finds a cache written by the previous version. Refusing it
	// would empty the cache exactly when relay needs it, so an older version must still read. The
	// body is an old-format environment with no key arrays: ToParams synthesizes them, which is what
	// makes the older shape readable rather than merely parseable.
	body := `{"envID":"env-abc","envKey":"my-env","envName":"My Env","projKey":"my-proj",` +
		`"projName":"My Proj","sdkKey":{"value":"sdk-anchor"},"mobKey":"mob-primary","version":3}`
	raw, err := json.Marshal(CachedItem{
		Kind:         ModelKindEnvironment,
		ModelVersion: CurrentModelVersion - 1,
		Data:         json.RawMessage(body),
	})
	require.NoError(t, err)

	item, err := unmarshalCachedItem(raw)
	require.NoError(t, err, "an entry written by the previous model version must still be readable")

	var rep envfactory.EnvironmentRep
	require.NoError(t, json.Unmarshal(item.Data, &rep))
	params := rep.ToParams()
	assert.Equal(t, config.SDKKey("sdk-anchor"), params.SDKKey)
	require.Len(t, params.AcceptedSDKKeys, 1, "the anchor is synthesized into sdkKeys[]")
	assert.Equal(t, config.SDKKey("sdk-anchor"), params.AcceptedSDKKeys[0].Value)
	require.Len(t, params.AcceptedMobileKeys, 1, "the primary is synthesized into mobileKeys[]")
	assert.Equal(t, config.MobileKey("mob-primary"), params.AcceptedMobileKeys[0].Value)
}

func TestUnmarshalRejectsAMissingVersion(t *testing.T) {
	// An envelope with no modelVersion decodes to zero. Accepting it would make the version check
	// meaningless for anything that is not a real envelope.
	_, err := unmarshalCachedItem([]byte(`{"kind":"environment","data":{}}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported model version 0")
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
}
