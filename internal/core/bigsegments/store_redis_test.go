// +build big_segment_external_store_tests

package bigsegments

import (
	"context"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envKey = "abc"
const testPrefix = "prefix"

func makeStore(t *testing.T) *redisBigSegmentStore {
	store, err := newRedisBigSegmentStore("127.0.0.1:6379", testPrefix, true, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	require.NoError(t, store.client.FlushAll(context.Background()).Err())
	return store
}

func withRedisStore(t *testing.T, action func(*redisBigSegmentStore)) {
	store := makeStore(t)
	defer store.Close()
	action(store)
}

func TestGetCursorUnset(t *testing.T) {
	withRedisStore(t, func(store *redisBigSegmentStore) {
		cursor, err := store.getCursor()
		require.NoError(t, err)
		assert.Equal(t, "", cursor)
	})
}

func TestGetSetSynchronizedOn(t *testing.T) {
	withRedisStore(t, func(store *redisBigSegmentStore) {
		synchronizedOn, err := store.GetSynchronizedOn()
		assert.Nil(t, synchronizedOn)
		require.NoError(t, err)
		timestamp := ldtime.UnixMillisecondTime(1000)
		err = store.setSynchronizedOn(timestamp)
		require.NoError(t, err)
		synchronizedOn, err = store.GetSynchronizedOn()
		assert.Equal(t, timestamp, synchronizedOn)
		require.NoError(t, err)
	})
}

func TestApplyPatchSequence(t *testing.T) {
	withRedisStore(t, func(store *redisBigSegmentStore) {
		// apply initial patch that adds users
		err := store.applyPatch(patch1)
		require.NoError(t, err)

		cursor, err := store.getCursor()
		require.NoError(t, err)
		require.Equal(t, patch1.Version, cursor)

		membership, err := store.client.SIsMember(context.Background(), bigSegmentsIncludeKey(testPrefix, "included1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, true, membership)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsExcludeKey(testPrefix, "excluded1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, true, membership)

		// apply second patch in sequence that removes users
		err = store.applyPatch(patch2)
		require.NoError(t, err)

		cursor, err = store.getCursor()
		require.NoError(t, err)
		assert.Equal(t, patch2.Version, cursor)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsIncludeKey(testPrefix, "included1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, false, membership)

		membership, err = store.client.SIsMember(context.Background(), bigSegmentsExcludeKey(testPrefix, "excluded1"), patch1.SegmentID).Result()
		require.NoError(t, err)
		assert.Equal(t, false, membership)

		// apply old patch
		err = store.applyPatch(patch1)
		require.NoError(t, err)

		cursor, err = store.getCursor()
		require.NoError(t, err)
		assert.Equal(t, patch2.Version, cursor)
	})
}

func TestSetSynchronizedOn(t *testing.T) {
	withRedisStore(t, func(store *redisBigSegmentStore) {
		timestamp := ldtime.UnixMillisecondTime(1005)
		err := store.setSynchronizedOn(timestamp)
		require.NoError(t, err)

		externalValue, err := store.client.Get(context.Background(), bigSegmentsSynchronizedKey(envKey)).Result()
		require.NoError(t, err)
		assert.Equal(t, externalValue, "1005")
	})
}
