// +build big_segment_external_store_tests

package bigsegments

import (
	"context"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

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
	store, err := newRedisBigSegmentStore("redis://127.0.0.1:6379", testPrefix, true, ldlog.NewDisabledLoggers())
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
			return store.client.SIsMember(context.Background(), bigSegmentsIncludeKey(testPrefix, userKey), segmentKey).Result()
		},
		isUserExcluded: func(segmentKey string, userKey string) (bool, error) {
			return store.client.SIsMember(context.Background(), bigSegmentsExcludeKey(testPrefix, userKey), segmentKey).Result()
		},
	}
}
