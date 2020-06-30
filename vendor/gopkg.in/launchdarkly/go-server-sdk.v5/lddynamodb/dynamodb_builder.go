package lddynamodb

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// DataStoreBuilder is a builder for configuring the DynamoDB-based persistent data store.
//
// Obtain an instance of this type by calling DataStore(). After calling its methods to specify any
// desired custom settings, wrap it in a PersistentDataStoreBuilder by calling
// ldcomponents.PersistentDataStore(), and then store this in the SDK configuration's DataStore field.
//
// Builder calls can be chained, for example:
//
//     config.DataStore = lddynamodb.DataStore("tablename").SessionOptions(someOption).Prefix("prefix")
//
// You do not need to call the builder's CreatePersistentDataStore() method yourself to build the
// actual data store; that will be done by the SDK.
type DataStoreBuilder struct {
	client         dynamodbiface.DynamoDBAPI
	table          string
	prefix         string
	configs        []*aws.Config
	sessionOptions session.Options
}

// DataStore returns a configurable builder for a DynamoDB-backed data store.
//
// The tableName parameter is required, and the table must already exist in DynamoDB.
func DataStore(tableName string) *DataStoreBuilder {
	return &DataStoreBuilder{
		table: tableName,
	}
}

// Prefix specifies a prefix for namespacing the data store's keys.
func (b *DataStoreBuilder) Prefix(prefix string) *DataStoreBuilder {
	b.prefix = prefix
	return b
}

// ClientConfig adds an AWS configuration object for the DynamoDB client. This allows you to customize
// settings such as the retry behavior.
func (b *DataStoreBuilder) ClientConfig(config *aws.Config) *DataStoreBuilder {
	if config != nil {
		b.configs = append(b.configs, config)
	}
	return b
}

// DynamoClient specifies an existing DynamoDB client instance. Use this if you want to customize the client
// used by the data store in ways that are not supported by other DataStoreBuilder options. If you
// specify this option, then any configurations specified with SessionOptions or ClientConfig will be ignored.
func (b *DataStoreBuilder) DynamoClient(client dynamodbiface.DynamoDBAPI) *DataStoreBuilder {
	b.client = client
	return b
}

// SessionOptions specifies an AWS Session.Options object to use when creating the DynamoDB session. This
// can be used to set properties such as the region programmatically, rather than relying on the defaults
// from the environment.
func (b *DataStoreBuilder) SessionOptions(options session.Options) *DataStoreBuilder {
	b.sessionOptions = options
	return b
}

// CreatePersistentDataStore is called internally by the SDK to create the data store implementation object.
func (b *DataStoreBuilder) CreatePersistentDataStore(
	context interfaces.ClientContext,
) (interfaces.PersistentDataStore, error) {
	store, err := newDynamoDBDataStoreImpl(b, context.GetLogging().GetLoggers())
	return store, err
}

// DescribeConfiguration is used internally by the SDK to inspect the configuration.
func (b *DataStoreBuilder) DescribeConfiguration() ldvalue.Value {
	return ldvalue.String("DynamoDB")
}
