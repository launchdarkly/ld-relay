// +build big_segment_external_store_tests

package bigsegments

import (
	"strconv"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bigSegmentOperations struct {
	isUserIncluded func(segmentKey string, userKey string) (bool, error)
	isUserExcluded func(segmentKey string, userKey string) (bool, error)
}

func testGenericAll(
	t *testing.T,
	withBigSegmentStore func(t *testing.T, action func(BigSegmentStore, bigSegmentOperations)),
) {
	patch1 := newPatchBuilder("segment.g1", "1", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	patch2 := newPatchBuilder("segment.g1", "2", "1").
		removeIncludes("included1").removeExcludes("excluded1").build()

	t.Run("synchronizedOn", func(t *testing.T) {
		withBigSegmentStore(t, func(store BigSegmentStore, operations bigSegmentOperations) {
			sync1, err := store.GetSynchronizedOn()
			require.NoError(t, err)
			assert.False(t, sync1.IsDefined())

			now := ldtime.UnixMillisNow()
			err = store.setSynchronizedOn(now)
			require.NoError(t, err)

			sync2, err := store.GetSynchronizedOn()
			require.NoError(t, err)
			require.Equal(t, now, sync2)
		})
	})

	t.Run("applyPatchSequence", func(t *testing.T) {
		withBigSegmentStore(t, func(store BigSegmentStore, operations bigSegmentOperations) {
			// first set synchronizedOn, so we can verify that applying a patch does *not* change that value
			initialSyncTime := ldtime.UnixMillisecondTime(99999)
			require.NoError(t, store.setSynchronizedOn(initialSyncTime))

			// apply initial patch that adds users
			success, err := store.applyPatch(patch1)
			require.NoError(t, err)
			require.True(t, success)

			cursor, err := store.getCursor()
			require.NoError(t, err)
			require.Equal(t, patch1.Version, cursor)

			membership, err := operations.isUserIncluded(patch1.SegmentID, patch1.Changes.Included.Add[0])
			require.NoError(t, err)
			assert.Equal(t, true, membership)

			membership, err = operations.isUserExcluded(patch1.SegmentID, patch1.Changes.Excluded.Add[0])
			require.NoError(t, err)
			assert.Equal(t, true, membership)

			// apply second patch in sequence that removes users
			success, err = store.applyPatch(patch2)
			require.NoError(t, err)
			require.True(t, success)

			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch2.Version, cursor)

			membership, err = operations.isUserIncluded(patch1.SegmentID, patch1.Changes.Included.Add[0])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			membership, err = operations.isUserExcluded(patch1.SegmentID, patch1.Changes.Excluded.Add[0])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			// apply old patch
			success, err = store.applyPatch(patch1)
			require.NoError(t, err)
			require.False(t, success)

			// verify that the stored cursor was updated
			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch2.Version, cursor)

			// verify that the sync time is still there
			syncTime, err := store.GetSynchronizedOn()
			require.NoError(t, err)
			assert.Equal(t, initialSyncTime, syncTime)

			// now update the sync time and verify that that doesn't affect the cursor
			newSyncTime := initialSyncTime + 1
			require.NoError(t, store.setSynchronizedOn(newSyncTime))
			syncTime, err = store.GetSynchronizedOn()
			require.NoError(t, err)
			assert.Equal(t, newSyncTime, syncTime)
			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch2.Version, cursor)
		})
	})

	t.Run("patchLarge", func(t *testing.T) {
		withBigSegmentStore(t, func(store BigSegmentStore, operations bigSegmentOperations) {
			userCount := 50
			var users []string = make([]string, 0, userCount)
			for i := 0; i < userCount; i++ {
				users = append(users, strconv.FormatUint(uint64(i), 10))
			}

			patch := bigSegmentPatch{
				EnvironmentID:   "abc",
				SegmentID:       "segment.g1",
				Version:         "1",
				PreviousVersion: "",
				Changes: bigSegmentPatchChanges{
					Included: bigSegmentPatchChangesMutations{
						Add: users,
					},
				},
			}

			success, err := store.applyPatch(patch)
			require.NoError(t, err)
			require.True(t, success)

			membership, err := operations.isUserIncluded(patch.SegmentID, strconv.FormatUint(uint64(userCount-1), 10))
			assert.Equal(t, true, membership)
			require.NoError(t, err)

			cursor, err := store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch.Version, cursor)
		})
	})
}
