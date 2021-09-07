package bigsegments

import (
	"context"
	"fmt"
	"strconv"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/go-redis/redis/v8"
)

func redisLockKey(prefix string) string {
	return fmt.Sprintf("%s:big_segments_lock", prefix)
}

func redisCursorKey(prefix string) string {
	return fmt.Sprintf("%s:big_segments_cursor", prefix)
}

func redisIncludeKey(prefix string, userHashKey string) string {
	return fmt.Sprintf("%s:big_segment_include:%s", prefix, userHashKey)
}

func redisExcludeKey(prefix string, userHashKey string) string {
	return fmt.Sprintf("%s:big_segment_exclude:%s", prefix, userHashKey)
}

func redisSynchronizedKey(prefix string) string {
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
func (r *redisBigSegmentStore) applyPatch(patch bigSegmentPatch) (bool, error) {
	ctx := context.Background()

	updated := false

	err := r.client.Watch(ctx, func(tx *redis.Tx) error {
		cursor, err := r.client.Get(ctx, redisCursorKey(r.prefix)).Result()
		if err != nil && err != redis.Nil {
			return err
		}

		if err == nil && cursor != patch.PreviousVersion {
			return err
		}

		result, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			err = pipe.Set(ctx, redisLockKey(r.prefix), "", 0).Err()
			if err != nil {
				return err
			}

			err = pipe.Set(ctx, redisCursorKey(r.prefix), patch.Version, 0).Err()
			if err != nil {
				return err
			}

			for _, v := range patch.Changes.Included.Add {
				err := pipe.SAdd(ctx, redisIncludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Included.Remove {
				err := pipe.SRem(ctx, redisIncludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Add {
				err := pipe.SAdd(ctx, redisExcludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			for _, v := range patch.Changes.Excluded.Remove {
				err := pipe.SRem(ctx, redisExcludeKey(r.prefix, v), patch.SegmentID).Err()
				if err != nil {
					return err
				}
			}

			return nil
		})
		if err != nil {
			return nil
		}
		for _, r := range result {
			if r.Err() != nil {
				return r.Err()
			}
		}
		updated = true
		return nil
	}, redisLockKey(r.prefix))

	return updated, err
}

func (r *redisBigSegmentStore) getCursor() (string, error) {
	cursor, err := r.client.Get(context.Background(), redisCursorKey(r.prefix)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return cursor, nil
}

func (r *redisBigSegmentStore) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	unixMilliseconds := strconv.FormatUint(uint64(synchronizedOn), 10)
	return r.client.Set(context.Background(), redisSynchronizedKey(r.prefix), unixMilliseconds, 0).Err()
}

func (r *redisBigSegmentStore) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	synchronizedOn, err := r.client.Get(context.Background(), redisSynchronizedKey(r.prefix)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	milliseconds, err := strconv.ParseInt(synchronizedOn, 10, 64)
	if err != nil {
		return 0, err
	}
	return ldtime.UnixMillisecondTime(milliseconds), nil
}

func (r *redisBigSegmentStore) Close() error {
	return r.client.Close()
}
