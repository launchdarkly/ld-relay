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

// redisBigSegmentStore implements BigSegmentStore for redis.
type redisBigSegmentStore struct {
	client  redis.UniversalClient
	prefix  string
	loggers ldlog.Loggers
}

// newRedisBigSegmentStore creates an instance of RedisBigSegmentStore.
func newRedisBigSegmentStore(
	url string,
	prefix string,
	checkOnStartup bool,
	loggers ldlog.Loggers,
) (*redisBigSegmentStore, error) {
	opts := redis.UniversalOptions{}

	parsed, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opts.DB = parsed.DB
	opts.Addrs = []string{parsed.Addr}
	opts.Username = parsed.Username
	opts.Password = parsed.Password
	opts.TLSConfig = parsed.TLSConfig

	store := redisBigSegmentStore{
		client:  redis.NewUniversalClient(&opts),
		prefix:  prefix,
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
func (r *redisBigSegmentStore) applyPatch(patch bigSegmentPatch) error {
	ctx := context.Background()

	err := r.client.Watch(ctx, func(tx *redis.Tx) error {
		cursor, err := r.client.Get(ctx, bigSegmentsCursorKey(r.prefix)).Result()
		if err != nil && err != redis.Nil {
			return err
		}

		if err != redis.Nil && cursor != patch.PreviousVersion {
			return nil
		}

		result, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			err = pipe.Set(ctx, bigSegmentsLockKey(r.prefix), "", 0).Err()
			if err != nil {
				return err
			}

			err = pipe.Set(ctx, bigSegmentsCursorKey(r.prefix), patch.Version, 0).Err()
			if err != nil {
				return err
			}

			for _, v := range patch.Changes.Included.Add {
				err := pipe.SAdd(ctx, bigSegmentsIncludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Included.Remove {
				err := pipe.SRem(ctx, bigSegmentsIncludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Add {
				err := pipe.SAdd(ctx, bigSegmentsExcludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Remove {
				err := pipe.SRem(ctx, bigSegmentsExcludeKey(r.prefix, v), patch.SegmentID).Err()
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
	}, bigSegmentsLockKey(r.prefix))

	return err
}

func (r *redisBigSegmentStore) getCursor() (string, error) {
	cursor, err := r.client.Get(context.Background(), bigSegmentsCursorKey(r.prefix)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return cursor, nil
}

func (r *redisBigSegmentStore) setSynchronizedOn(synchronizedOn time.Time) error {
	unixMilliseconds := strconv.FormatUint(uint64(ldtime.UnixMillisFromTime(synchronizedOn)), 10)
	return r.client.Set(context.Background(), bigSegmentsSynchronizedKey(r.prefix), unixMilliseconds, 0).Err()
}

func (r *redisBigSegmentStore) Close() error {
	return r.client.Close()
}
