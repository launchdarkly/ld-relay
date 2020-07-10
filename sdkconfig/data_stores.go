package sdkconfig

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	redigo "github.com/garyburd/redigo/redis"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldconsul"
	"gopkg.in/launchdarkly/go-server-sdk.v5/lddynamodb"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldredis"
)

// ConfigureDataStore provides the appropriate Go SDK data store factory (in-memory, Redis, etc.) based on
// the Relay configuration, or returns an error if the configuration is invalid.
func ConfigureDataStore(
	allConfig config.Config,
	envConfig config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.DataStoreFactory, error) {
	var dbFactory interfaces.DataStoreFactory

	useRedis := allConfig.Redis.Url != "" || allConfig.Redis.Host != ""
	useConsul := allConfig.Consul.Host != ""
	useDynamoDB := allConfig.DynamoDB.Enabled
	countTrue := func(values ...bool) int {
		n := 0
		for _, v := range values {
			if v {
				n++
			}
		}
		return n
	}
	if countTrue(useRedis, useConsul, useDynamoDB) > 1 {
		return nil, errors.New("Cannot enable more than one database at a time (Redis, DynamoDB, Consul)")
	}
	if useRedis {
		dbConfig := allConfig.Redis
		redisURL := dbConfig.Url
		if dbConfig.Host != "" {
			if redisURL != "" {
				loggers.Warnf("Both a URL and a hostname were specified for Redis; will use the URL")
			} else {
				port := dbConfig.Port
				if port == 0 {
					port = 6379
				}
				redisURL = fmt.Sprintf("redis://%s:%d", dbConfig.Host, port)
			}
		}
		loggers.Infof("Using Redis feature store: %s with prefix: %s\n", redisURL, envConfig.Prefix)

		dialOptions := []redigo.DialOption{}
		if dbConfig.Tls || (dbConfig.Password != "") {
			if dbConfig.Tls {
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
			CacheTime(time.Duration(dbConfig.LocalTtl) * time.Millisecond)
	}
	if useConsul {
		dbConfig := allConfig.Consul
		loggers.Infof("Using Consul feature store: %s with prefix: %s", dbConfig.Host, envConfig.Prefix)
		dbFactory = ldcomponents.PersistentDataStore(
			ldconsul.DataStore().
				Address(dbConfig.Host).
				Prefix(envConfig.Prefix),
		).CacheTime(time.Duration(dbConfig.LocalTtl) * time.Millisecond)
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
		if dbConfig.Url != "" {
			awsOptions := session.Options{
				Config: aws.Config{
					Endpoint: aws.String(dbConfig.Url),
				},
			}
			builder.SessionOptions(awsOptions)
		}
		dbFactory = ldcomponents.PersistentDataStore(builder).
			CacheTime(time.Duration(dbConfig.LocalTtl) * time.Millisecond)
	}

	if dbFactory != nil {
		return dbFactory, nil
	}
	return ldcomponents.InMemoryDataStore(), nil
}
