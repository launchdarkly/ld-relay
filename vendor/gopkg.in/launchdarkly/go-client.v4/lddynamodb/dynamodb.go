// Package lddynamodb provides a DynamoDB-backed feature store for the LaunchDarkly Go SDK.
//
// For more details about how and why you can use a persistent feature store, see:
// https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store
//
// To use the DynamoDB feature store with the LaunchDarkly client:
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore("my-table-name")
//     if err != nil { ... }
//
//     config := ld.DefaultConfig
//     config.FeatureStore = store
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// Note that the specified table must already exist in DynamoDB. It must have a
// partition key of "namespace", and a sort key of "key".
//
// By default, the feature store uses a basic DynamoDB client configuration that is
// equivalent to doing this:
//
//     dynamoClient := dynamodb.New(session.NewSession())
//
// This default configuration will only work if your AWS credentials and region are
// available from AWS environment variables and/or configuration files. If you want to
// set those programmatically or modify any other configuration settings, you can use
// the SessionOptions function, or use an already-configured client via the DynamoClient
// function.
//
// If you are using the same DynamoDB table as a feature store for multiple LaunchDarkly
// environments, use the Prefix option and choose a different prefix string for each, so
// they will not interfere with each other's data.
package lddynamodb

// This is based on code from https://github.com/mlafeldt/launchdarkly-dynamo-store.
// Changes include a different method of configuration, a different method of marshaling
// objects, less potential for race conditions, and unit tests that run against a local
// Dynamo instance.

// Implementation notes:
//
// - Feature flags, segments, and any other kind of entity the LaunchDarkly client may wish
// to store, are all put in the same table. The only two required attributes are "key" (which
// is present in all storeable entities) and "namespace" (a parameter from the client that is
// used to disambiguate between flags and segments).
//
// - Because of DynamoDB's restrictions on attribute values (e.g. empty strings are not
// allowed), the standard DynamoDB marshaling mechanism with one attribute per object property
// is not used. Instead, the entire object is serialized to JSON and stored in a single
// attribute, "item". The "version" property is also stored as a separate attribute since it
// is used for updates.
//
// - Since DynamoDB doesn't have transactions, the Init method - which replaces the entire data
// store - is not atomic, so there can be a race condition if another process is adding new data
// via Upsert. To minimize this, we don't delete all the data at the start; instead, we update
// the items we've received, and then delete all other items. That could potentially result in
// deleting new data from another process, but that would be the case anyway if the Init
// happened to execute later than the Upsert; we are relying on the fact that normally the
// process that did the Init will also receive the new data shortly and do its own Upsert.
//
// - DynamoDB has a maximum item size of 400KB. Since each feature flag or user segment is
// stored as a single item, this mechanism will not work for extremely large flags or segments.

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
	ld "gopkg.in/launchdarkly/go-client.v4"
	"gopkg.in/launchdarkly/go-client.v4/utils"
)

const (
	// DefaultCacheTTL is the amount of time that recently read or updated items will be cached
	// in memory, unless you specify otherwise with the CacheTTL option.
	DefaultCacheTTL = 15 * time.Second
)

const (
	// Schema of the DynamoDB table
	tablePartitionKey = "namespace"
	tableSortKey      = "key"
	versionAttribute  = "version"
	itemJSONAttribute = "item"
)

type namespaceAndKey struct {
	namespace string
	key       string
}

// Internal type for our DynamoDB implementation of the ld.FeatureStore interface.
type dynamoDBFeatureStore struct {
	client         dynamodbiface.DynamoDBAPI
	table          string
	prefix         string
	cacheTTL       time.Duration
	configs        []*aws.Config
	sessionOptions session.Options
	logger         ld.Logger
	testUpdateHook func() // Used only by unit tests - see updateWithVersioning
}

// FeatureStoreOption is the interface for optional configuration parameters that can be
// passed to NewDynamoDBFeatureStore. These include SessionOptions, CacheTTL, DynamoClient,
// and Logger.
type FeatureStoreOption interface {
	apply(store *dynamoDBFeatureStore) error
}

type prefixOption struct {
	prefix string
}

func (o prefixOption) apply(store *dynamoDBFeatureStore) error {
	store.prefix = o.prefix
	return nil
}

// Prefix creates an option for NewDynamoDBFeatureStore to specify a string
// that should be prepended to all partition keys used by the feature store. A colon will be
// added to this automatically. If this is unspecified, no prefix will be used.
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore(lddynamodb.Prefix("ld-data"))
func Prefix(prefix string) FeatureStoreOption {
	return prefixOption{prefix}
}

type cacheTTLOption struct {
	cacheTTL time.Duration
}

func (o cacheTTLOption) apply(store *dynamoDBFeatureStore) error {
	store.cacheTTL = o.cacheTTL
	return nil
}

// CacheTTL creates an option for NewDynamoDBFeatureStore to set the amount of time
// that recently read or updated items should remain in an in-memory cache. This reduces the
// amount of database access if the same feature flags are being evaluated repeatedly. If it
// is zero, there will be no in-memory caching. The default value is DefaultCacheTTL.
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore("my-table-name", lddynamodb.CacheTTL(30*time.Second))
func CacheTTL(ttl time.Duration) FeatureStoreOption {
	return cacheTTLOption{ttl}
}

type clientConfigOption struct {
	config *aws.Config
}

func (o clientConfigOption) apply(store *dynamoDBFeatureStore) error {
	store.configs = append(store.configs, o.config)
	return nil
}

// ClientConfig creates an option for NewDynamoDBFeatureStore to add an AWS configuration
// object for the DynamoDB client. This allows you to customize settings such as the
// retry behavior.
func ClientConfig(config *aws.Config) FeatureStoreOption {
	return clientConfigOption{config}
}

type dynamoClientOption struct {
	client dynamodbiface.DynamoDBAPI
}

func (o dynamoClientOption) apply(store *dynamoDBFeatureStore) error {
	store.client = o.client
	return nil
}

// DynamoClient creates an option for NewDynamoDBFeatureStore to specify an existing
// DynamoDB client instance. Use this if you want to customize the client used by the
// feature store in ways that are not supported by other NewDynamoDBFeatureStore options.
// If you specify this option, then any configurations specified with SessionOptions or
// ClientConfig will be ignored.
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore("my-table-name", lddynamodb.DynamoClient(myDBClient))
func DynamoClient(client dynamodbiface.DynamoDBAPI) FeatureStoreOption {
	return dynamoClientOption{client}
}

type sessionOptionsOption struct {
	options session.Options
}

func (o sessionOptionsOption) apply(store *dynamoDBFeatureStore) error {
	store.sessionOptions = o.options
	return nil
}

// SessionOptions creates an option for NewDynamoDBFeatureStore, to specify an AWS
// Session.Options object to use when creating the DynamoDB session. This can be used to
// set properties such as the region programmatically, rather than relying on the
// defaults from the environment.
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore("my-table-name", lddynamodb.SessionOptions(myOptions))
func SessionOptions(options session.Options) FeatureStoreOption {
	return sessionOptionsOption{options}
}

type loggerOption struct {
	logger ld.Logger
}

func (o loggerOption) apply(store *dynamoDBFeatureStore) error {
	store.logger = o.logger
	return nil
}

// Logger creates an option for NewDynamoDBFeatureStore, to specify where to send log output.
// If not specified, a log.Logger is used.
//
//     store, err := lddynamodb.NewDynamoDBFeatureStore("my-table-name", lddynamodb.Logger(myLogger))
func Logger(logger ld.Logger) FeatureStoreOption {
	return loggerOption{logger}
}

// NewDynamoDBFeatureStore creates a new DynamoDB feature store to be used by the LaunchDarkly client.
//
// By default, this function uses https://docs.aws.amazon.com/sdk-for-go/api/aws/session/#NewSession
// to configure access to DynamoDB, so the configuration will use your local AWS credentials as well
// as AWS environment variables. You can also override the default configuration with the SessionOptions
// option, or use an already-configured DynamoDB client instance with the DynamoClient option.
//
// For other options that can be customized, see CacheTTL and Logger.
func NewDynamoDBFeatureStore(table string, options ...FeatureStoreOption) (ld.FeatureStore, error) {
	store, err := newDynamoDBFeatureStoreInternal(table, options...)
	if err != nil {
		return nil, err
	}
	return utils.NewFeatureStoreWrapper(store), nil
}

func newDynamoDBFeatureStoreInternal(table string, options ...FeatureStoreOption) (*dynamoDBFeatureStore, error) {
	store := dynamoDBFeatureStore{
		table:    table,
		cacheTTL: DefaultCacheTTL,
	}

	for _, o := range options {
		err := o.apply(&store)
		if err != nil {
			return nil, err
		}
	}

	if store.logger == nil {
		store.logger = log.New(os.Stderr, "[LaunchDarkly DynamoDBFeatureStore]", log.LstdFlags)
	}

	if store.client == nil {
		sess, err := session.NewSessionWithOptions(store.sessionOptions)
		if err != nil {
			return nil, err
		}
		store.client = dynamodb.New(sess, store.configs...)
	}

	return &store, nil
}

func (store *dynamoDBFeatureStore) GetCacheTTL() time.Duration {
	return store.cacheTTL
}

func (store *dynamoDBFeatureStore) InitInternal(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	// Start by reading the existing keys; we will later delete any of these that weren't in allData.
	unusedOldKeys, err := store.readExistingKeys(allData)
	if err != nil {
		store.logger.Printf("ERROR: Failed to get existing items prior to Init: %s", err)
		return err
	}

	requests := make([]*dynamodb.WriteRequest, 0)
	numItems := 0

	// Insert or update every provided item
	for kind, items := range allData {
		for k, v := range items {
			av, err := store.marshalItem(kind, v)
			if err != nil {
				store.logger.Printf("ERROR: Failed to marshal item (key=%s): %s", k, err)
				return err
			}
			requests = append(requests, &dynamodb.WriteRequest{
				PutRequest: &dynamodb.PutRequest{Item: av},
			})
			nk := namespaceAndKey{namespace: store.namespaceForKind(kind), key: v.GetKey()}
			unusedOldKeys[nk] = false
			numItems++
		}
	}

	// Now delete any previously existing items whose keys were not in the current data
	initedKey := store.initedKey()
	for k, v := range unusedOldKeys {
		if v && k.namespace != initedKey {
			delKey := map[string]*dynamodb.AttributeValue{
				tablePartitionKey: &dynamodb.AttributeValue{S: aws.String(k.namespace)},
				tableSortKey:      &dynamodb.AttributeValue{S: aws.String(k.key)},
			}
			requests = append(requests, &dynamodb.WriteRequest{
				DeleteRequest: &dynamodb.DeleteRequest{Key: delKey},
			})
		}
	}

	// Now set the special key that we check in InitializedInternal()
	initedItem := map[string]*dynamodb.AttributeValue{
		tablePartitionKey: &dynamodb.AttributeValue{S: aws.String(initedKey)},
		tableSortKey:      &dynamodb.AttributeValue{S: aws.String(initedKey)},
	}
	requests = append(requests, &dynamodb.WriteRequest{
		PutRequest: &dynamodb.PutRequest{Item: initedItem},
	})

	if err := batchWriteRequests(store.client, store.table, requests); err != nil {
		store.logger.Printf("ERROR: Failed to write %d item(s) in batches: %s", len(requests), err)
		return err
	}

	store.logger.Printf("INFO: Initialized table %q with %d item(s)", store.table, numItems)

	return nil
}

func (store *dynamoDBFeatureStore) InitializedInternal() bool {
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(store.initedKey())},
			tableSortKey:      {S: aws.String(store.initedKey())},
		},
	})
	return err == nil && len(result.Item) != 0
}

func (store *dynamoDBFeatureStore) GetAllInternal(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {
	var items []map[string]*dynamodb.AttributeValue

	err := store.client.QueryPages(store.makeQueryForKind(kind),
		func(out *dynamodb.QueryOutput, lastPage bool) bool {
			items = append(items, out.Items...)
			return !lastPage
		})
	if err != nil {
		store.logger.Printf("ERROR: Failed to get all %q items: %s", kind.GetNamespace(), err)
		return nil, err
	}

	results := make(map[string]ld.VersionedData)

	for _, i := range items {
		item, err := unmarshalItem(kind, i)
		if err != nil {
			store.logger.Printf("ERROR: Failed to unmarshal item: %s", err)
			return nil, err
		}
		results[item.GetKey()] = item
	}

	return results, nil
}

func (store *dynamoDBFeatureStore) GetInternal(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(store.namespaceForKind(kind))},
			tableSortKey:      {S: aws.String(key)},
		},
	})
	if err != nil {
		store.logger.Printf("ERROR: Failed to get item (key=%s): %s", key, err)
		return nil, err
	}

	if len(result.Item) == 0 {
		store.logger.Printf("DEBUG: Item not found (key=%s)", key)
		return nil, nil
	}

	item, err := unmarshalItem(kind, result.Item)
	if err != nil {
		store.logger.Printf("ERROR: Failed to unmarshal item (key=%s): %s", key, err)
		return nil, err
	}

	return item, nil
}

func (store *dynamoDBFeatureStore) UpsertInternal(kind ld.VersionedDataKind, item ld.VersionedData) (ld.VersionedData, error) {
	av, err := store.marshalItem(kind, item)
	if err != nil {
		store.logger.Printf("ERROR: Failed to marshal item (key=%s): %s", item.GetKey(), err)
		return nil, err
	}

	if store.testUpdateHook != nil {
		store.testUpdateHook()
	}

	_, err = store.client.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(store.table),
		Item:      av,
		ConditionExpression: aws.String(
			"attribute_not_exists(#namespace) or " +
				"attribute_not_exists(#key) or " +
				":version > #version",
		),
		ExpressionAttributeNames: map[string]*string{
			"#namespace": aws.String(tablePartitionKey),
			"#key":       aws.String(tableSortKey),
			"#version":   aws.String(versionAttribute),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":version": &dynamodb.AttributeValue{N: aws.String(strconv.Itoa(item.GetVersion()))},
		},
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException {
			store.logger.Printf("DEBUG: Not updating item due to condition (namespace=%s key=%s version=%d)",
				kind.GetNamespace(), item.GetKey(), item.GetVersion())
			// We must now read the item that's in the database and return it, so FeatureStoreWrapper can cache it
			oldItem, err := store.GetInternal(kind, item.GetKey())
			return oldItem, err
		}
		store.logger.Printf("ERROR: Failed to put item (namespace=%s key=%s): %s", kind.GetNamespace(), item.GetKey(), err)
		return nil, err
	}

	return item, nil
}

func (store *dynamoDBFeatureStore) prefixedNamespace(baseNamespace string) string {
	if store.prefix == "" {
		return baseNamespace
	}
	return store.prefix + ":" + baseNamespace
}

func (store *dynamoDBFeatureStore) namespaceForKind(kind ld.VersionedDataKind) string {
	return store.prefixedNamespace(kind.GetNamespace())
}

func (store *dynamoDBFeatureStore) initedKey() string {
	return store.prefixedNamespace("$inited")
}

func (store *dynamoDBFeatureStore) makeQueryForKind(kind ld.VersionedDataKind) *dynamodb.QueryInput {
	return &dynamodb.QueryInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		KeyConditions: map[string]*dynamodb.Condition{
			tablePartitionKey: {
				ComparisonOperator: aws.String("EQ"),
				AttributeValueList: []*dynamodb.AttributeValue{
					{S: aws.String(store.namespaceForKind(kind))},
				},
			},
		},
	}
}

func (store *dynamoDBFeatureStore) readExistingKeys(newData map[ld.VersionedDataKind]map[string]ld.VersionedData) (map[namespaceAndKey]bool, error) {
	keys := make(map[namespaceAndKey]bool)
	for kind := range newData {
		query := store.makeQueryForKind(kind)
		query.ProjectionExpression = aws.String("#namespace, #key")
		query.ExpressionAttributeNames = map[string]*string{
			"#namespace": aws.String(tablePartitionKey),
			"#key":       aws.String(tableSortKey),
		}
		err := store.client.QueryPages(query,
			func(out *dynamodb.QueryOutput, lastPage bool) bool {
				for _, i := range out.Items {
					nk := namespaceAndKey{namespace: *(*i[tablePartitionKey]).S, key: *(*i[tableSortKey]).S}
					keys[nk] = true
				}
				return !lastPage
			})
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// batchWriteRequests executes a list of write requests (PutItem or DeleteItem)
// in batches of 25, which is the maximum BatchWriteItem can handle.
func batchWriteRequests(client dynamodbiface.DynamoDBAPI, table string, requests []*dynamodb.WriteRequest) error {
	for len(requests) > 0 {
		batchSize := int(math.Min(float64(len(requests)), 25))
		batch := requests[:batchSize]
		requests = requests[batchSize:]

		_, err := client.BatchWriteItem(&dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]*dynamodb.WriteRequest{table: batch},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (store *dynamoDBFeatureStore) marshalItem(kind ld.VersionedDataKind, item ld.VersionedData) (map[string]*dynamodb.AttributeValue, error) {
	jsonItem, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	return map[string]*dynamodb.AttributeValue{
		tablePartitionKey: &dynamodb.AttributeValue{S: aws.String(store.namespaceForKind(kind))},
		tableSortKey:      &dynamodb.AttributeValue{S: aws.String(item.GetKey())},
		versionAttribute:  &dynamodb.AttributeValue{N: aws.String(strconv.Itoa(item.GetVersion()))},
		itemJSONAttribute: &dynamodb.AttributeValue{S: aws.String(string(jsonItem))},
	}, nil
}

func unmarshalItem(kind ld.VersionedDataKind, item map[string]*dynamodb.AttributeValue) (ld.VersionedData, error) {
	if itemAttr := item[itemJSONAttribute]; itemAttr != nil && itemAttr.S != nil {
		data, err := utils.UnmarshalItem(kind, []byte(*itemAttr.S))
		return data, err
	}
	return nil, errors.New("DynamoDB map did not contain expected item string")
}
