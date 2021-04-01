package bigsegments

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/go-redis/redis/v8"
)

func bigSegmentsLockKey(prefix string) string {
	return fmt.Sprintf("%s:big_segments_lock", prefix)
}

func bigSegmentsCursorKey(prefix string) string {
	return fmt.Sprintf("%s:big_segments_cursor", prefix)
}

func bigSegmentsIncludeKey(prefix string, userHashKey string) string {
	return fmt.Sprintf("%s:big_segment_include:%s", prefix, userHashKey)
}

func bigSegmentsExcludeKey(prefix string, userHashKey string) string {
	return fmt.Sprintf("%s:big_segment_exclude:%s", prefix, userHashKey)
}

func bigSegmentsSynchronizedKey(prefix string) string {
	return fmt.Sprintf("%s:big_segments_synchronized_on", prefix)
}

// RedisBigSegmentStore implements BigSegmentStore for redis.
type RedisBigSegmentStore struct {
	client  redis.UniversalClient
	loggers ldlog.Loggers
}

// NewRedisBigSegmentStore creates an instance of RedisBigSegmentStore.
func NewRedisBigSegmentStore(
	address string,
	checkOnStartup bool,
	loggers ldlog.Loggers,
) (BigSegmentStore, error) {
	opts := redis.UniversalOptions{
		Addrs: []string{address},
	}

	store := RedisBigSegmentStore{
		client:  redis.NewUniversalClient(&opts),
		loggers: loggers,
	}

	if checkOnStartup {
		err := store.client.Ping(context.Background()).Err()
		if err != nil {
			return nil, err
		}
	}

	store.loggers.SetPrefix("RedisBigSegmentStore:")

	return &store, nil
}

// applyPatch is used to apply updates to the store.
func (r *RedisBigSegmentStore) applyPatch(patch bigSegmentPatch) error {
	ctx := context.Background()

	err := r.client.Watch(ctx, func(tx *redis.Tx) error {
		cursor, err := r.client.Get(ctx, bigSegmentsCursorKey(patch.EnvironmentID)).Result()
		if err != nil && err != redis.Nil {
			return err
		}

		if err != redis.Nil && cursor != patch.PreviousVersion {
			return nil
		}

		result, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			err = pipe.Set(ctx, bigSegmentsLockKey(patch.EnvironmentID), "", 0).Err()
			if err != nil {
				return err
			}

			err = pipe.Set(ctx, bigSegmentsCursorKey(patch.EnvironmentID), patch.Version, 0).Err()
			if err != nil {
				return err
			}

			for _, v := range patch.Changes.Included.Add {
				err := pipe.SAdd(ctx, bigSegmentsIncludeKey(patch.EnvironmentID, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Included.Remove {
				err := pipe.SRem(ctx, bigSegmentsIncludeKey(patch.EnvironmentID, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Add {
				err := pipe.SAdd(ctx, bigSegmentsExcludeKey(patch.EnvironmentID, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Remove {
				err := pipe.SRem(ctx, bigSegmentsExcludeKey(patch.EnvironmentID, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			return nil
		})
		if err != nil {
			return nil
		}
		if len(result) > 0 {
			return result[0].Err()
		}
		return nil
	}, bigSegmentsLockKey(patch.EnvironmentID))

	return err
}

// getCursor loads the synchronization cursor from the external store.
func (r *RedisBigSegmentStore) getCursor(environmentID string) (string, error) {
	cursor, err := r.client.Get(context.Background(), bigSegmentsCursorKey(environmentID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return cursor, nil
}

// setSynchronizedOn stores the synchronization time in the external store
func (r *RedisBigSegmentStore) setSynchronizedOn(environmentID string, synchronizedOn time.Time) error {
	unixMilliseconds := strconv.FormatUint(uint64(ldtime.UnixMillisFromTime(synchronizedOn)), 10)
	return r.client.Set(context.Background(), bigSegmentsSynchronizedKey(environmentID), unixMilliseconds, 0).Err()
}

// Close shuts down the Redis client
func (r *RedisBigSegmentStore) Close() error {
	return r.client.Close()
}
