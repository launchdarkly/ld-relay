// +build big_segment_external_store_tests

package bigsegments

import (
	"context"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/stretchr/testify/require"
)

const (
	envKey     = "abc"
	testPrefix = "prefix"
)

func TestRedisGenericAll(t *testing.T) {
	testGenericAll(t, withRedisStoreGeneric)
}

func makeStore(t *testing.T) *redisBigSegmentStore {
	redisConfig := config.RedisConfig{}
	redisConfig.URL, _ = configtypes.NewOptURLAbsoluteFromString("redis://127.0.0.1:6379")
	store, err := newRedisBigSegmentStore(redisConfig, testPrefix, true, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	require.NoError(t, store.client.FlushAll(context.Background()).Err())
	return store
}

func withRedisStoreGeneric(t *testing.T, action func(BigSegmentStore, bigSegmentOperations)) {
	store := makeStore(t)
	defer store.Close()
	action(store, redisMakeOperations(store))
}

func redisMakeOperations(store *redisBigSegmentStore) bigSegmentOperations {
	return bigSegmentOperations{
		isUserIncluded: func(segmentKey string, userKey string) (bool, error) {
			return store.client.SIsMember(context.Background(), redisIncludeKey(testPrefix, userKey), segmentKey).Result()
		},
		isUserExcluded: func(segmentKey string, userKey string) (bool, error) {
			return store.client.SIsMember(context.Background(), redisExcludeKey(testPrefix, userKey), segmentKey).Result()
		},
	}
}
