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
			// apply initial patch that adds users
			err := store.applyPatch(patch1)
			require.NoError(t, err)

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
			err = store.applyPatch(patch2)
			require.NoError(t, err)

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
			err = store.applyPatch(patch1)
			require.Error(t, err)

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

			err := store.applyPatch(patch)
			require.NoError(t, err)

			membership, err := operations.isUserIncluded(patch.SegmentID, strconv.FormatUint(uint64(userCount-1), 10))
			assert.Equal(t, true, membership)
			require.NoError(t, err)

			cursor, err := store.getCursor()
			require.NoError(t, err)
			assert.Equal(t, patch.Version, cursor)
		})
	})
}
