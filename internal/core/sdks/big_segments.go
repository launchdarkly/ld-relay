package sdks

import (
	"github.com/launchdarkly/ld-relay/v6/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// ConfigureBigSegments provides the appropriate Go SDK big segments configuration based on the Relay
// configuration, or nil if big segments are not enabled. The big segments stores in Relay's SDK
// instances are used for client-side evaluations; server-side SDKs will read from the same database
// via their own big segments stores, which will need to be configured similarly to what's here.
func ConfigureBigSegments(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.BigSegmentsConfigurationFactory, error) {
	var storeFactory interfaces.BigSegmentStoreFactory

	if allConfig.Redis.URL.IsDefined() {
		redisBuilder, redisURL := makeRedisDataStoreBuilder(allConfig, envConfig)
		loggers.Infof("Using Redis big segment store: %s with prefix: %s", redisURL, envConfig.Prefix)
		storeFactory = redisBuilder
	} else if allConfig.DynamoDB.Enabled {
		dynamoDBBuilder, tableName, err := makeDynamoDBDataStoreBuilder(allConfig, envConfig)
		if err != nil {
			return nil, err
		}
		loggers.Infof("Using DynamoDB big segment store: %s with prefix: %s", tableName, envConfig.Prefix)
		storeFactory = dynamoDBBuilder
	}

	if storeFactory != nil {
		return ldcomponents.BigSegments(storeFactory), nil
	}
	return nil, nil
}
