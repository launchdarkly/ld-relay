package sdks

import (
	"github.com/launchdarkly/ld-relay/v6/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// ConfigureBigSegments provides the appropriate Go SDK big segments configuration based on the Relay
// configuration, or nil if big segments are not enabled. The big segments stores in Relay's SDK
// instances are used for client-side evaluations; server-side SDKs will read from the same database
// via their own big segments stores, which will need to be configured similarly to what's here.
//
// This method always returns either a configuration factory or nil. There is no error return
// because there aren't any invalid configuration conditions that wouldn't have already caused
// errors; if there's something in the inputs that we don't understand at this point, we can just
// ignore it and return nil. The configuration factory itself contains a mechanism for reporting
// errors (like inability to start a database client) later when the SDK client is started.
//
// The allowBigSegmentStatusQueries function allows us to override the SDK's mechanism for checking
// the the store metadata: if the function returns false (or is nil), all calls to the GetMetadata
// method of the BigSegmentStore will return fake metadata with an up-to-date timestamp rather than
// doing a database query. Relay will do this if it knows that there is not any big segment data
// yet, to prevent the SDK from doing pointless queries that would fail.
func ConfigureBigSegments(
	allConfig config.Config,
	envConfig config.EnvConfig,
	allowBigSegmentStatusQueries func() bool,
	loggers ldlog.Loggers,
) interfaces.BigSegmentsConfigurationFactory {
	var storeFactory interfaces.BigSegmentStoreFactory

	if allConfig.Redis.URL.IsDefined() {
		redisBuilder, redisURL := makeRedisDataStoreBuilder(allConfig, envConfig)
		loggers.Infof("Using Redis big segment store: %s with prefix: %s", redisURL, envConfig.Prefix)
		storeFactory = redisBuilder
	}

	if storeFactory != nil {
		return ldcomponents.BigSegments(
			bigSegmentsStoreWrapperFactory{
				wrappedFactory:               storeFactory,
				allowBigSegmentStatusQueries: allowBigSegmentStatusQueries,
			},
		)
	}
	return nil
}

type bigSegmentsStoreWrapper struct {
	wrappedStore                 interfaces.BigSegmentStore
	allowBigSegmentStatusQueries func() bool
}

type bigSegmentsStoreWrapperFactory struct {
	wrappedFactory               interfaces.BigSegmentStoreFactory
	allowBigSegmentStatusQueries func() bool
}

func (f bigSegmentsStoreWrapperFactory) CreateBigSegmentStore(
	context interfaces.ClientContext,
) (interfaces.BigSegmentStore, error) {
	store, err := f.wrappedFactory.CreateBigSegmentStore(context)
	if err != nil {
		return nil, err
	}
	return bigSegmentsStoreWrapper{
		wrappedStore:                 store,
		allowBigSegmentStatusQueries: f.allowBigSegmentStatusQueries,
	}, nil
}

func (s bigSegmentsStoreWrapper) Close() error {
	return s.wrappedStore.Close()
}

func (s bigSegmentsStoreWrapper) GetMetadata() (interfaces.BigSegmentStoreMetadata, error) {
	if s.allowBigSegmentStatusQueries != nil && s.allowBigSegmentStatusQueries() {
		return s.wrappedStore.GetMetadata()
	}
	return interfaces.BigSegmentStoreMetadata{
		LastUpToDate: ldtime.UnixMillisNow(),
	}, nil
}

func (s bigSegmentsStoreWrapper) GetUserMembership(userHash string) (interfaces.BigSegmentMembership, error) {
	return s.wrappedStore.GetUserMembership(userHash)
}
