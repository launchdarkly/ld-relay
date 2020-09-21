package sdks

import (
	"errors"
	"strings"

	ldconsul "github.com/launchdarkly/go-server-sdk-consul"
	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo"
	"github.com/launchdarkly/ld-relay/v6/config"
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

// ConfigureDataStore provides the appropriate Go SDK data store factory (in-memory, Redis, etc.) based on
// the Relay configuration. It can return an error for some invalid configurations, but it assumes that we
// have already done the standard validation steps defined in the config package.
func ConfigureDataStore(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.DataStoreFactory, error) {
	if allConfig.Redis.URL.IsDefined() {
		dbConfig := allConfig.Redis
		redisURL := dbConfig.URL.String()

		if dbConfig.TLS {
			if strings.HasPrefix(redisURL, "redis:") {
				// Redigo's DialUseTLS option will not work if you're specifying a URL.
				redisURL = "rediss:" + strings.TrimPrefix(redisURL, "redis:")
			}
		}

		loggers.Infof("Using Redis feature store: %s with prefix: %s", redisURL, envConfig.Prefix)

		var dialOptions []redigo.DialOption
		if dbConfig.Password != "" {
			dialOptions = append(dialOptions, redigo.DialPassword(dbConfig.Password))
		}

		builder := ldredis.DataStore().
			URL(redisURL).
			Prefix(envConfig.Prefix).
			DialOptions(dialOptions...)
		return ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), nil
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

		return ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), nil
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
			return nil, errDynamoDBWithNoTableName
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
		return ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), nil
	}

	return ldcomponents.InMemoryDataStore(), nil
}
