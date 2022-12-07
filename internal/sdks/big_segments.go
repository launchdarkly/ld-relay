package sdks

import (
	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"

	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb/v3"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v2"
)

// ConfigureBigSegments provides the appropriate Go SDK big segments configuration based on the Relay
// configuration, or nil if big segments are not enabled. The big segments stores in Relay's SDK
// instances are used for client-side evaluations; server-side SDKs will read from the same database
// via their own big segments stores, which will need to be configured similarly to what's here.
func ConfigureBigSegments(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (subsystems.ComponentConfigurer[subsystems.BigSegmentsConfiguration], error) {
	var storeFactory subsystems.ComponentConfigurer[subsystems.BigSegmentStore]

	if allConfig.Redis.URL.IsDefined() {
		redisBuilder, redisURL := makeRedisDataStoreBuilder(ldredis.BigSegmentStore, allConfig, envConfig)
		loggers.Infof("Using Redis big segment store: %s with prefix: %s", redisURL, envConfig.Prefix)
		storeFactory = redisBuilder
	} else if allConfig.DynamoDB.Enabled {
		dynamoDBBuilder, tableName, err := makeDynamoDBDataStoreBuilder(lddynamodb.BigSegmentStore, allConfig, envConfig)
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
