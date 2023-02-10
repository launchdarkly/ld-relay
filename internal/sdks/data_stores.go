package sdks

import (
	"context"
	"errors"
	"strings"

	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/util"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldconsul "github.com/launchdarkly/go-server-sdk-consul/v2"
	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb/v3"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v2"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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

	// DBServer is the URL or host address of the database server, if applicable. Passwords, if any,
	// must be redacted in this string.
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
) (subsystems.ComponentConfigurer[subsystems.DataStore], DataStoreEnvironmentInfo, error) {
	if allConfig.Redis.URL.IsDefined() {
		// Our config validation already takes care of normalizing the Redis parameters so that if a
		// host & port were specified, they are transformed into a URL.
		redisBuilder, redisURL := makeRedisDataStoreBuilder(ldredis.DataStore, allConfig, envConfig)
		redactedURL := util.RedactURL(redisURL)

		loggers.Infof("Using Redis data store: %s with prefix: %s", redactedURL, envConfig.Prefix)

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "redis",
			DBServer: redactedURL,
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
		loggers.Infof("Using Consul data store: %s with prefix: %s", dbConfig.Host, envConfig.Prefix)

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
		builder, tableName, err := makeDynamoDBDataStoreBuilder(lddynamodb.DataStore, allConfig, envConfig)
		if err != nil {
			return nil, DataStoreEnvironmentInfo{}, err
		}

		loggers.Infof("Using DynamoDB data store: %s with prefix: %s", tableName, envConfig.Prefix)

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "dynamodb",
			DBServer: allConfig.DynamoDB.URL.String(),
			DBPrefix: envConfig.Prefix,
			DBTable:  tableName,
		}

		return ldcomponents.PersistentDataStore(builder).
			CacheTime(allConfig.DynamoDB.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), storeInfo, nil
	}

	return ldcomponents.InMemoryDataStore(), DataStoreEnvironmentInfo{}, nil
}

// GetRedisBasicProperties transforms the configuration properties to the standard parameters
// used for Redis. This function is exported to ensure consistency between the SDK
// configuration and the internal big segment store for Redis.
func GetRedisBasicProperties(
	dbConfig config.RedisConfig,
	envConfig config.EnvConfig,
) (redisURL, prefix string) {
	redisURL = dbConfig.URL.String()

	if dbConfig.TLS {
		if strings.HasPrefix(redisURL, "redis:") {
			// Redigo's DialUseTLS option will not work if you're specifying a URL.
			redisURL = "rediss:" + strings.TrimPrefix(redisURL, "redis:")
		}
	}

	prefix = envConfig.Prefix
	if prefix == "" {
		prefix = ldredis.DefaultPrefix
	}

	return
}

func makeRedisDataStoreBuilder[T any](
	constructor func() *ldredis.StoreBuilder[T],
	allConfig config.Config,
	envConfig config.EnvConfig,
) (builder *ldredis.StoreBuilder[T], url string) {
	redisURL, prefix := GetRedisBasicProperties(allConfig.Redis, envConfig)

	var dialOptions []redigo.DialOption
	if allConfig.Redis.Password != "" {
		dialOptions = append(dialOptions, redigo.DialPassword(allConfig.Redis.Password))
	}
	if allConfig.Redis.Username != "" {
		dialOptions = append(dialOptions, redigo.DialUsername(allConfig.Redis.Username))
	}

	b := constructor().
		URL(redisURL).
		Prefix(prefix).
		DialOptions(dialOptions...)
	return b, redisURL
}

// GetDynamoDBBasicProperties transforms the configuration properties to the standard parameters
// used for DynamoDB. This function is exported to ensure consistency between the SDK
// configuration and the internal big segment store for DynamoDB.
func GetDynamoDBBasicProperties(
	dbConfig config.DynamoDBConfig,
	envConfig config.EnvConfig,
) (endpoint *string, tableName, prefix string) {
	// Note that the global TableName can be omitted if you specify a TableName for each environment
	// (this is why we need an Enabled property here, since the other properties are all optional).
	// You can also specify a prefix for each environment, as with the other databases.
	tableName = envConfig.TableName
	if tableName == "" {
		tableName = dbConfig.TableName
	}

	prefix = envConfig.Prefix

	if dbConfig.URL.IsDefined() {
		endpoint = aws.String(dbConfig.URL.String())
	}

	return
}

func makeDynamoDBDataStoreBuilder[T any](
	constructor func(string) *lddynamodb.StoreBuilder[T],
	allConfig config.Config,
	envConfig config.EnvConfig,
) (*lddynamodb.StoreBuilder[T], string, error) {
	endpoint, tableName, prefix := GetDynamoDBBasicProperties(allConfig.DynamoDB, envConfig)
	if tableName == "" {
		return nil, "", errDynamoDBWithNoTableName
	}
	builder := constructor(tableName).
		Prefix(prefix)
	config, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, "", err
	}
	var options []func(*dynamodb.Options)
	if endpoint != nil {
		options = append(options, func(o *dynamodb.Options) {
			o.EndpointResolver = dynamodb.EndpointResolverFromURL(*endpoint)
		})
	}
	builder.ClientConfig(config, options...)
	return builder, tableName, nil
}
