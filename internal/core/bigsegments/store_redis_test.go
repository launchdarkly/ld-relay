// +build big_segment_external_store_tests

package bigsegments

import (
	"context"
	"testing"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envKey = "abc"

func makeStore(t *testing.T) *RedisBigSegmentStore {
	storeInterface, err := NewRedisBigSegmentStore("127.0.0.1:6379", true, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	store := storeInterface.(*RedisBigSegmentStore)
	require.NoError(t, store.client.FlushAll(context.Background()).Err())
	return store
}

func withRedisStore(t *testing.T, action func(*RedisBigSegmentStore)) {
	store := makeStore(t)
	defer store.Close()
	action(store)
}

func TestGetCursorUnset(t *testing.T) {
	withRedisStore(t, func(store *RedisBigSegmentStore) {
		cursor, err := store.getCursor(envKey)
		require.NoError(t, err)
		assert.Equal(t, "", cursor)
	})
}

func TestApplyPatchSequence(t *testing.T) {
	withRedisStore(t, func(store *RedisBigSegmentStore) {
		// apply initial patch that adds users
		err := store.applyPatch(patch1)
		require.NoError(t, err)

		cursor, err := store.getCursor(envKey)
		require.NoError(t, err)
		require.Equal(t, patch1.Version, cursor)

		membership, err := store.client.SIsMember(context.Background(), bigSegmentsIncludeKey(envKey, "included1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, true, membership)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsExcludeKey(envKey, "excluded1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, true, membership)

		// apply second patch in sequence that removes users
		err = store.applyPatch(patch2)
		require.NoError(t, err)

		cursor, err = store.getCursor(envKey)
		require.NoError(t, err)
		assert.Equal(t, patch2.Version, cursor)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsIncludeKey(envKey, "included1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, false, membership)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsExcludeKey(envKey, "excluded1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, false, membership)

		// apply old patch
		err = store.applyPatch(patch1)
		require.NoError(t, err)

		cursor, err = store.getCursor(envKey)
		require.NoError(t, err)
		assert.Equal(t, patch2.Version, cursor)
	})
}

func TestSetSynchronizedOn(t *testing.T) {
	withRedisStore(t, func(store *RedisBigSegmentStore) {
		timestamp := time.Unix(1000, 1000000 * 5)
		err := store.setSynchronizedOn(envKey, timestamp)
		require.NoError(t, err)

		externalValue, err := store.client.Get(context.Background(), bigSegmentsSynchronizedKey(envKey)).Result()
		require.NoError(t, err)
		assert.Equal(t, externalValue, "1000005")
	})
}
