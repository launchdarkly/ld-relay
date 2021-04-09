package sdks

import (
	"errors"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/config"

	ldconsul "github.com/launchdarkly/go-server-sdk-consul"
	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	redigo "github.com/gomodule/redigo/redis"
	consul "github.com/hashicorp/consul/api"
)

var (
	errDynamoDBWithNoTableName = errors.New("TableName property must be specified for DynamoDB, either globally or per environment")
)

// DataStoreEnvironmentInfo encapsulates database-related configuration details that we will expose in the
// status resource for a specific environment. Some of these are set on a per-environment basis and others
// are global.
type DataStoreEnvironmentInfo struct {
	// DBType is the type of database Relay is using, or "" for the default in-memory storage.
	DBType string

	// DBServer is the URL or host address of the database server, if applicable.
	DBServer string

	// DBPrefix is the key prefix used for this environment to distinguish it from data that might be in
	// the same database for other environments. This is required for Redis and Consul but optional for
	// DynamoDB.
	DBPrefix string

	// DBTable is the table name for this environment if using DynamoDB, or "" otherwise.
	DBTable string
}

// ConfigureDataStore provides the appropriate Go SDK data store factory (in-memory, Redis, etc.) based on
// the Relay configuration. It can return an error for some invalid configurations, but it assumes that we
// have already done the standard validation steps defined in the config package.
func ConfigureDataStore(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.DataStoreFactory, DataStoreEnvironmentInfo, error) {
	if allConfig.Redis.URL.IsDefined() {
		// Our config validation already takes care of normalizing the Redis parameters so that if a
		// host & port were specified, they are transformed into a URL.
		redisBuilder, redisURL := makeRedisDataStoreBuilder(allConfig, envConfig)

		loggers.Infof("Using Redis feature store: %s with prefix: %s", redisURL, envConfig.Prefix)

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "redis",
			DBServer: redisURL,
			DBPrefix: envConfig.Prefix,
		}
		if storeInfo.DBPrefix == "" {
			storeInfo.DBPrefix = ldredis.DefaultPrefix
		}

		return ldcomponents.PersistentDataStore(redisBuilder).
			CacheTime(allConfig.Redis.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), storeInfo, nil
	}

	if allConfig.Consul.Host != "" {
		dbConfig := allConfig.Consul
		loggers.Infof("Using Consul feature store: %s with prefix: %s", dbConfig.Host, envConfig.Prefix)

		builder := ldconsul.DataStore().
			Prefix(envConfig.Prefix)
		if dbConfig.Token != "" {
			builder.Config(consul.Config{Token: dbConfig.Token})
		} else if dbConfig.TokenFile != "" {
			builder.Config(consul.Config{TokenFile: dbConfig.TokenFile})
		}
		builder.Address(dbConfig.Host) // this is deliberately done last so it's not overridden by builder.Config()

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "consul",
			DBServer: dbConfig.Host,
			DBPrefix: envConfig.Prefix,
		}
		if storeInfo.DBPrefix == "" {
			storeInfo.DBPrefix = ldconsul.DefaultPrefix
		}

		return ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), storeInfo, nil
	}

	if allConfig.DynamoDB.Enabled {
		// Note that the global TableName can be omitted if you specify a TableName for each environment
		// (this is why we need an Enabled property here, since the other properties are all optional).
		// You can also specify a prefix for each environment, as with the other databases.
		dbConfig := allConfig.DynamoDB
		tableName := envConfig.TableName
		if tableName == "" {
			tableName = dbConfig.TableName
		}
		if tableName == "" {
			return nil, DataStoreEnvironmentInfo{}, errDynamoDBWithNoTableName
		}
		loggers.Infof("Using DynamoDB feature store: %s with prefix: %s", tableName, envConfig.Prefix)
		builder := lddynamodb.DataStore(tableName).
			Prefix(envConfig.Prefix)
		if dbConfig.URL.IsDefined() {
			awsOptions := session.Options{
				Config: aws.Config{
					Endpoint: aws.String(dbConfig.URL.String()),
				},
			}
			builder.SessionOptions(awsOptions)
		}

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "dynamodb",
			DBServer: dbConfig.URL.String(),
			DBPrefix: envConfig.Prefix,
			DBTable:  tableName,
		}

		return ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), storeInfo, nil
	}

	return ldcomponents.InMemoryDataStore(), DataStoreEnvironmentInfo{}, nil
}

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
func ConfigureBigSegments(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) interfaces.BigSegmentsConfigurationFactory {
	if allConfig.Redis.URL.IsDefined() {
		redisBuilder, redisURL := makeRedisDataStoreBuilder(allConfig, envConfig)
		loggers.Infof("Using Redis big segment store: %s with prefix: %s", redisURL, envConfig.Prefix)
		return ldcomponents.BigSegments(redisBuilder)
	}

	return nil
}

func makeRedisDataStoreBuilder(
	allConfig config.Config,
	envConfig config.EnvConfig,
) (builder *ldredis.DataStoreBuilder, url string) {
	dbConfig := allConfig.Redis
	redisURL := dbConfig.URL.String()

	if dbConfig.TLS {
		if strings.HasPrefix(redisURL, "redis:") {
			// Redigo's DialUseTLS option will not work if you're specifying a URL.
			redisURL = "rediss:" + strings.TrimPrefix(redisURL, "redis:")
		}
	}

	var dialOptions []redigo.DialOption
	if dbConfig.Password != "" {
		dialOptions = append(dialOptions, redigo.DialPassword(dbConfig.Password))
	}

	b := ldredis.DataStore().
		URL(redisURL).
		Prefix(envConfig.Prefix).
		DialOptions(dialOptions...)
	return b, redisURL
}
