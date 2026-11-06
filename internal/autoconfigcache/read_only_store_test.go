package autoconfigcache

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
)

type recordingStore struct {
	noopStore
	gets   int
	writes int
}

func (s *recordingStore) GetAll(context.Context) (*autoconfig.PutContent, error) {
	s.gets++
	return &autoconfig.PutContent{}, nil
}

func (s *recordingStore) SetAll(context.Context, autoconfig.PutContent) error {
	s.writes++
	return nil
}

func (s *recordingStore) Upsert(context.Context, autoconfig.CacheKind, string, interface{}) error {
	s.writes++
	return nil
}

func (s *recordingStore) Delete(context.Context, autoconfig.CacheKind, string) error {
	s.writes++
	return nil
}

func TestReadOnlyStoreReadsButDoesNotWrite(t *testing.T) {
	inner := &recordingStore{}
	store := readOnlyStore{inner}
	ctx := context.Background()

	content, err := store.GetAll(ctx)
	require.NoError(t, err)
	assert.NotNil(t, content)
	require.NoError(t, store.SetAll(ctx, autoconfig.PutContent{}))
	require.NoError(t, store.Upsert(ctx, autoconfig.CacheKindEnvironment, "env", nil))
	require.NoError(t, store.Delete(ctx, autoconfig.CacheKindEnvironment, "env"))

	assert.Equal(t, 1, inner.gets)
	assert.Equal(t, 0, inner.writes)
}
