package bigsegments

import (
	"io"

	"github.com/launchdarkly/ld-relay/v6/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/aws/aws-sdk-go/aws"
)

// BigSegmentStore is the interface for interacting with an external big segment store. Each instance
// is specific to one LD environment.
type BigSegmentStore interface {
	io.Closer

	// The rest of the interface methods are non-exported because they're only relevant within
	// this package, and no implementations of the interface are created outside of this package.

	// applyPatch is used to apply updates to the store.
	applyPatch(patch bigSegmentPatch) error
	// getCursor loads the synchronization cursor from the external store.
	getCursor() (string, error)
	// setSynchronizedOn stores the synchronization time in the external store
	setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error
	// GetSynchronizedOn returns the synchronization time from the external store.
	//
	// The synchronization time may not exist in the store. Use `IsDefined()` to
	// check the result.
	GetSynchronizedOn() (ldtime.UnixMillisecondTime, error)
}

// BigSegmentStoreFactory creates an implementation of BigSegmentStore, if the configuration
// implies that we should have one; if not, it returns nil.
type BigSegmentStoreFactory func(
	envConfig config.EnvConfig,
	allConfig config.Config,
	loggers ldlog.Loggers,
) (BigSegmentStore, error)

// DefaultBigSegmentStoreFactory implements our standard logic for optionally creating a
// BigSegmentStore.
func DefaultBigSegmentStoreFactory(
	envConfig config.EnvConfig,
	allConfig config.Config,
	loggers ldlog.Loggers,
) (BigSegmentStore, error) {
	// Currently, the only supported store type is Redis, and if Redis is enabled then big segments
	// are enabled.
	if allConfig.Redis.URL.IsDefined() {
		bigSegmentRedis, err := newRedisBigSegmentStore(allConfig.Redis.URL.String(), envConfig.Prefix, false, loggers)
		if err != nil {
			return nil, err
		}
		return bigSegmentRedis, nil
	} else if allConfig.DynamoDB.Enabled {
		dbConfig := allConfig.DynamoDB
		return newDynamoDBBigSegmentStore(
			dbConfig.URL, aws.Config{}, loggers, dbConfig.TableName, envConfig.Prefix)
	}
	return nil, nil
}

// NewNullBigSegmentStore returns a no-op stub implementation. This is used only in tests, but it is
// exported from this package so that we can keep the interface methods private.
func NewNullBigSegmentStore() BigSegmentStore {
	return &nullBigSegmentStore{}
}

type nullBigSegmentStore struct{}

func (s *nullBigSegmentStore) Close() error { return nil }

func (s *nullBigSegmentStore) applyPatch(patch bigSegmentPatch) error { return nil }

func (s *nullBigSegmentStore) getCursor() (string, error) { return "", nil }

func (s *nullBigSegmentStore) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	return nil
}

func (s *nullBigSegmentStore) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	return 0, nil
}
