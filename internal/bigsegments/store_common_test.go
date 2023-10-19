//go:build big_segment_external_store_tests
// +build big_segment_external_store_tests

package bigsegments

import (
	"strconv"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

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
	// When syncing from a real big segments endpoint the first series of patches will be from a paginated
	// API. The "version" and "previousVersion" will be blank for the first page of entries excluding the final
	// entry. At which point the "version" becomes a cursor used to request the next page of entries.
	// Each subsequent page will have a "version" equal to the "after" parameter of the request. The "previous" version
	// will be the version from the previous request.
	//
	// For each version several patches may be cumulatively applied to that version. So the empty version may
	// receive several patches, as may each subsequent version.
	//
	// So it could be imagined that patch1, patch2, and patch 3 represent the first page of results returned from
	// the API. First creating the empty version, then updating it, and then creating a new version by updating the cursor.
	// Patch4 would represent the second page of results.
	patch1 := newPatchBuilder("segment.g1", "", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	patch2 := newPatchBuilder("segment.g1", "", "").
		addIncludes("included3", "included4").addExcludes("excluded3", "excluded4").build()
	patch3 := newPatchBuilder("segment.g1", "652ec9b74997e612346d9482", "").
		removeIncludes("included1").removeExcludes("excluded1").build()
	patch4 := newPatchBuilder("segment.g1", "4b748182441b808549a03c19", "652ec9b74997e612346d9482").
		removeIncludes("included2").removeExcludes("excluded2").build()

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

			// second patch adds some more users
			success, err = store.applyPatch(patch2)
			require.NoError(t, err)
			require.True(t, success)

			cursor, err = store.getCursor()
			require.NoError(t, err)
			require.Equal(t, patch2.Version, cursor)

			membership, err = operations.isUserIncluded(patch2.SegmentID, patch2.Changes.Included.Add[0])
			require.NoError(t, err)
			assert.Equal(t, true, membership)

			membership, err = operations.isUserExcluded(patch2.SegmentID, patch2.Changes.Excluded.Add[0])
			require.NoError(t, err)
			assert.Equal(t, true, membership)

			// apply third patch in sequence that removes users
			success, err = store.applyPatch(patch3)
			require.NoError(t, err)
			require.True(t, success)

			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch3.Version, cursor)

			membership, err = operations.isUserIncluded(patch1.SegmentID, patch1.Changes.Included.Add[0])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			membership, err = operations.isUserExcluded(patch1.SegmentID, patch1.Changes.Excluded.Add[0])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			// apply a fourth patch in sequence that removes more users
			success, err = store.applyPatch(patch4)
			require.NoError(t, err)
			require.True(t, success)

			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch4.Version, cursor)

			membership, err = operations.isUserIncluded(patch1.SegmentID, patch1.Changes.Included.Add[1])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			membership, err = operations.isUserExcluded(patch1.SegmentID, patch1.Changes.Excluded.Add[1])
			require.NoError(t, err)
			assert.Equal(t, false, membership)

			// apply old patch
			success, err = store.applyPatch(patch1)
			require.NoError(t, err)
			require.False(t, success)

			// verify that the stored cursor was not updated
			cursor, err = store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch4.Version, cursor)

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
			assert.Equal(t, patch4.Version, cursor)
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
