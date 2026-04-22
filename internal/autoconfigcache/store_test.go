package autoconfigcache

import (
	"context"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheField(t *testing.T) {
	assert.Equal(t, "env:abc123", cacheField(autoconfig.CacheKindEnvironment, "abc123"))
	assert.Equal(t, "filter:f1", cacheField(autoconfig.CacheKindFilter, "f1"))
}

func TestNoopStore(t *testing.T) {
	s := noopStore{}

	content, err := s.GetAll(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, content)

	assert.NoError(t, s.SetAll(context.Background(), autoconfig.PutContent{}))
	assert.NoError(t, s.Upsert(context.Background(), autoconfig.CacheKindEnvironment, "id", struct{}{}))
	assert.NoError(t, s.Delete(context.Background(), autoconfig.CacheKindEnvironment, "id"))
	assert.NoError(t, s.Close())
}

func TestMergeContextCancelledByParent(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	store := context.Background()

	merged, cleanup := mergeContext(parent, store)
	defer cleanup()

	parentCancel()

	select {
	case <-merged.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("merged context should have been cancelled by parent")
	}
}

func TestMergeContextCancelledByStore(t *testing.T) {
	parent := context.Background()
	store, storeCancel := context.WithCancel(context.Background())

	merged, cleanup := mergeContext(parent, store)
	defer cleanup()

	storeCancel()

	select {
	case <-merged.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("merged context should have been cancelled by store")
	}
}

func TestMergeContextCleanup(t *testing.T) {
	parent := context.Background()
	store := context.Background()

	merged, cleanup := mergeContext(parent, store)
	cleanup()

	// After cleanup, the merged context should be cancelled.
	require.Error(t, merged.Err())
}
