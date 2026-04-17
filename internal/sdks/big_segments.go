package sdks

import (
	"log/slog"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb/v4"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v3"
)

// ConfigureBigSegments provides the appropriate Go SDK big segments configuration based on the Relay
// configuration, or nil if big segments are not enabled. The big segments stores in Relay's SDK
// instances are used for client-side evaluations; server-side SDKs will read from the same database
// via their own big segments stores, which will need to be configured similarly to what's here.
func ConfigureBigSegments(
	allConfig config.Config,
	envConfig config.EnvConfig,
	logger *slog.Logger,
) (subsystems.ComponentConfigurer[subsystems.BigSegmentsConfiguration], error) {
	var storeFactory subsystems.ComponentConfigurer[subsystems.BigSegmentStore]

	if allConfig.Redis.URL.IsDefined() {
		redisBuilder, redisURL := makeRedisDataStoreBuilder(ldredis.BigSegmentStore, allConfig, envConfig)
		redactedURL := util.RedactURL(redisURL)
		logger.Info("using Redis big segment store", "url", redactedURL, "prefix", envConfig.Prefix)
		storeFactory = redisBuilder
	} else if allConfig.DynamoDB.Enabled {
		dynamoDBBuilder, tableName, err := makeDynamoDBDataStoreBuilder(lddynamodb.BigSegmentStore, allConfig, envConfig)
		if err != nil {
			return nil, err
		}
		logger.Info("using DynamoDB big segment store", "table", tableName, "prefix", envConfig.Prefix)
		storeFactory = dynamoDBBuilder
	}

	if storeFactory != nil {
		return ldcomponents.BigSegments(storeFactory), nil
	}
	return nil, nil
}
