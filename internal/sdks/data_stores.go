package sdks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	ldconsul "github.com/launchdarkly/go-server-sdk-consul/v3"
	lddynamodb "github.com/launchdarkly/go-server-sdk-dynamodb/v4"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v4"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	redigo "github.com/gomodule/redigo/redis"
	consul "github.com/hashicorp/consul/api"
)

var errDynamoDBWithNoTableName = errors.New("TableName property must be specified for DynamoDB, either globally or per environment")

// DataStoreEnvironmentInfo encapsulates database-related configuration details that we will expose in the
// status resource for a specific environment. Some of these are set on a per-environment basis and others
// are global.
type DataStoreEnvironmentInfo struct {
	// DBType is the type of database Relay is using, or "" for the default in-memory storage.
	DBType string

	// DBServer is the URL or host address of the database server, if applicable. Credentials, if any,
	// must be redacted in this string with util.RedactURL, since it is exposed by the unauthenticated
	// status resource.
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
	logger *slog.Logger,
) (subsystems.ComponentConfigurer[subsystems.DataStore], DataStoreEnvironmentInfo, error) {
	if allConfig.Redis.URL.IsDefined() {
		// Our config validation already takes care of normalizing the Redis parameters so that if a
		// host & port were specified, they are transformed into a URL.
		redisURL, prefix, dialOptions, err := getRedisBuilderOptions(allConfig, envConfig)
		if err != nil {
			return nil, DataStoreEnvironmentInfo{}, err
		}
		upsertMode := ldredis.UpsertModeWatch
		if allConfig.Redis.AtomicUpsert {
			upsertMode = ldredis.UpsertModeAtomicScript
		}
		redisBuilder := ldredis.DataStore().
			URL(redisURL).
			Prefix(prefix).
			DialOptions(dialOptions...).
			UpsertMode(upsertMode)
		redactedURL := util.RedactURL(redisURL)

		logger.Info("using Redis data store", "url", redactedURL, "prefix", envConfig.Prefix)

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
		redactedHost := util.RedactURL(dbConfig.Host)
		logger.Info("using Consul data store", "host", redactedHost, "prefix", envConfig.Prefix)

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
			DBServer: redactedHost,
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

		logger.Info("using DynamoDB data store", "table", tableName, "prefix", envConfig.Prefix)

		storeInfo := DataStoreEnvironmentInfo{
			DBType:   "dynamodb",
			DBServer: util.RedactURL(allConfig.DynamoDB.URL.String()),
			DBPrefix: envConfig.Prefix,
			DBTable:  tableName,
		}

		return ldcomponents.PersistentDataStore(builder).
			CacheTime(allConfig.DynamoDB.LocalTTL.GetOrElse(config.DefaultDatabaseCacheTTL)), storeInfo, nil
	}

	return nil, DataStoreEnvironmentInfo{}, nil
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

// CreateTLSConfig creates a TLS configuration for Redis based on the provided RedisConfig.
// It returns nil if TLS is not enabled in the configuration (neither REDIS_TLS nor a rediss:// URL).
// If TLS is enabled, it sets up the TLS configuration with the specified server name, minimum version,
// if a client certificate, key and CA file are provided, it loads them into the TLS configuration.
func CreateTLSConfig(config config.RedisConfig) (*tls.Config, error) {
	if !config.TLSEnabled() {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		ServerName: config.URL.Get().Hostname(),
		MinVersion: tls.VersionTLS12,
	}

	if config.ClientCertificateFile != "" && config.ClientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(config.ClientCertificateFile, config.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading Redis client certificate %q and key %q: %w",
				config.ClientCertificateFile, config.ClientKeyFile, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if config.CAFile != "" {
		caCert, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading Redis CA file: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("no valid PEM certificates in Redis CA file %q", config.CAFile)
		}
		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

// getRedisBuilderOptions returns the parameters that the Redis data store and the Redis big segment
// store both use.
func getRedisBuilderOptions(
	allConfig config.Config,
	envConfig config.EnvConfig,
) (redisURL, prefix string, dialOptions []redigo.DialOption, err error) {
	redisURL, prefix = GetRedisBasicProperties(allConfig.Redis, envConfig)

	if allConfig.Redis.Password != "" {
		dialOptions = append(dialOptions, redigo.DialPassword(allConfig.Redis.Password))
	}
	if allConfig.Redis.Username != "" {
		dialOptions = append(dialOptions, redigo.DialUsername(allConfig.Redis.Username))
	}

	tlsOpts, err := CreateTLSConfig(allConfig.Redis)
	if err != nil {
		return "", "", nil, err
	}
	if tlsOpts != nil {
		dialOptions = append(dialOptions, redigo.DialUseTLS(true), redigo.DialTLSConfig(tlsOpts))
	}
	return
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
			o.BaseEndpoint = endpoint
		})
	}
	builder.ClientConfig(config, options...)
	return builder, tableName, nil
}
