package sdkconfig

import (
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	redigo "github.com/gomodule/redigo/redis"

	ldconsul "github.com/launchdarkly/go-server-sdk-consul"
	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb"
	ldredis "github.com/launchdarkly/go-server-sdk-redis"
	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// ConfigureDataStore provides the appropriate Go SDK data store factory (in-memory, Redis, etc.) based on
// the Relay configuration, or returns an error if the configuration is invalid.
func ConfigureDataStore(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.DataStoreFactory, error) {
	var dbFactory interfaces.DataStoreFactory

	useRedis := allConfig.Redis.URL.IsDefined()
	useConsul := allConfig.Consul.Host != ""
	useDynamoDB := allConfig.DynamoDB.Enabled

	if useRedis {
		dbConfig := allConfig.Redis
		redisURL := dbConfig.URL.String()
		loggers.Infof("Using Redis feature store: %s with prefix: %s\n", redisURL, envConfig.Prefix)

		dialOptions := []redigo.DialOption{}
		if dbConfig.TLS || (dbConfig.Password != "") {
			if dbConfig.TLS {
				if strings.HasPrefix(redisURL, "redis:") {
					// Redigo's DialUseTLS option will not work if you're specifying a URL.
					redisURL = "rediss:" + strings.TrimPrefix(redisURL, "redis:")
				}
			}
			if dbConfig.Password != "" {
				dialOptions = append(dialOptions, redigo.DialPassword(dbConfig.Password))
			}
		}

		builder := ldredis.DataStore().
			URL(redisURL).
			Prefix(envConfig.Prefix).
			DialOptions(dialOptions...)
		dbFactory = ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL))
	}
	if useConsul {
		dbConfig := allConfig.Consul
		loggers.Infof("Using Consul feature store: %s with prefix: %s", dbConfig.Host, envConfig.Prefix)
		dbFactory = ldcomponents.PersistentDataStore(
			ldconsul.DataStore().
				Address(dbConfig.Host).
				Prefix(envConfig.Prefix),
		).CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL))
	}
	if useDynamoDB {
		// Note that the global TableName can be omitted if you specify a TableName for each environment
		// (this is why we need an Enabled property here, since the other properties are all optional).
		// You can also specify a prefix for each environment, as with the other databases.
		dbConfig := allConfig.DynamoDB
		tableName := envConfig.TableName
		if tableName == "" {
			tableName = dbConfig.TableName
		}
		if tableName == "" {
			return nil, errors.New("TableName property must be specified for DynamoDB, either globally or per environment")
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
		dbFactory = ldcomponents.PersistentDataStore(builder).
			CacheTime(dbConfig.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL))
	}

	if dbFactory != nil {
		return dbFactory, nil
	}
	return ldcomponents.InMemoryDataStore(), nil
}
