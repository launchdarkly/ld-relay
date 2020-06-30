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
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
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

// Internal type for our DynamoDB implementation of the ld.DataStore interface.
type dynamoDBDataStore struct {
	client         dynamodbiface.DynamoDBAPI
	table          string
	prefix         string
	loggers        ldlog.Loggers
	testUpdateHook func() // Used only by unit tests - see updateWithVersioning
}

func newDynamoDBDataStoreImpl(builder *DataStoreBuilder, loggers ldlog.Loggers) (*dynamoDBDataStore, error) {
	if builder.table == "" {
		return nil, errors.New("table name is required")
	}

	store := &dynamoDBDataStore{
		client:  builder.client,
		table:   builder.table,
		prefix:  builder.prefix,
		loggers: loggers, // copied by value so we can modify it
	}
	store.loggers.SetPrefix("DynamoDBDataStore:")
	store.loggers.Infof(`Using DynamoDB table %s`, store.table)

	if store.client == nil {
		sess, err := session.NewSessionWithOptions(builder.sessionOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to configure DynamoDB client: %s", err)
		}
		store.client = dynamodb.New(sess, builder.configs...)
	}

	return store, nil
}

func (store *dynamoDBDataStore) Init(allData []interfaces.StoreSerializedCollection) error {
	// Start by reading the existing keys; we will later delete any of these that weren't in allData.
	unusedOldKeys, err := store.readExistingKeys(allData)
	if err != nil {
		return fmt.Errorf("failed to get existing items prior to Init: %s", err)
	}

	requests := make([]*dynamodb.WriteRequest, 0)
	numItems := 0

	// Insert or update every provided item
	for _, coll := range allData {
		for _, item := range coll.Items {
			av := store.encodeItem(coll.Kind, item.Key, item.Item)
			requests = append(requests, &dynamodb.WriteRequest{
				PutRequest: &dynamodb.PutRequest{Item: av},
			})
			nk := namespaceAndKey{namespace: store.namespaceForKind(coll.Kind), key: item.Key}
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
		// COVERAGE: can't cause an error here in unit tests because we only get this far if the
		// DynamoDB client is successful on the initial query
		return fmt.Errorf("failed to write %d items(s) in batches: %s", len(requests), err)
	}

	store.loggers.Infof("Initialized table %q with %d item(s)", store.table, numItems)

	return nil
}

func (store *dynamoDBDataStore) IsInitialized() bool {
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

func (store *dynamoDBDataStore) GetAll(
	kind interfaces.StoreDataKind,
) ([]interfaces.StoreKeyedSerializedItemDescriptor, error) {
	var results []interfaces.StoreKeyedSerializedItemDescriptor
	err := store.client.QueryPages(store.makeQueryForKind(kind),
		func(out *dynamodb.QueryOutput, lastPage bool) bool {
			for _, item := range out.Items {
				if key, serializedItemDesc, ok := store.decodeItem(item); ok {
					results = append(results, interfaces.StoreKeyedSerializedItemDescriptor{
						Key:  key,
						Item: serializedItemDesc,
					})
				}
			}
			return !lastPage
		})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (store *dynamoDBDataStore) Get(
	kind interfaces.StoreDataKind,
	key string,
) (interfaces.StoreSerializedItemDescriptor, error) {
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(store.namespaceForKind(kind))},
			tableSortKey:      {S: aws.String(key)},
		},
	})
	if err != nil {
		return interfaces.StoreSerializedItemDescriptor{}.NotFound(),
			fmt.Errorf("failed to get %s key %s: %s", kind, key, err)
	}

	if len(result.Item) == 0 {
		if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
			store.loggers.Debugf("Item not found (key=%s)", key)
		}
		return interfaces.StoreSerializedItemDescriptor{}.NotFound(), nil
	}

	if _, serializedItemDesc, ok := store.decodeItem(result.Item); ok {
		return serializedItemDesc, nil
	}
	return interfaces.StoreSerializedItemDescriptor{}.NotFound(), // COVERAGE: can't cause this in unit tests
		fmt.Errorf("invalid data for %s key %s: %s", kind, key, err)
}

func (store *dynamoDBDataStore) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	newItem interfaces.StoreSerializedItemDescriptor,
) (bool, error) {
	av := store.encodeItem(kind, key, newItem)

	if store.testUpdateHook != nil {
		store.testUpdateHook()
	}

	_, err := store.client.PutItem(&dynamodb.PutItemInput{
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
			":version": &dynamodb.AttributeValue{N: aws.String(strconv.Itoa(newItem.Version))},
		},
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException {
			if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
				store.loggers.Debugf("Not updating item due to condition (namespace=%s key=%s version=%d)",
					kind, key, newItem.Version)
			}
			return false, nil
		}
		return false, fmt.Errorf("failed to put %s key %s: %s", kind, key, err)
	}

	return true, nil
}

func (store *dynamoDBDataStore) IsStoreAvailable() bool {
	// There doesn't seem to be a specific DynamoDB API for just testing the connection. We will just
	// do a simple query for the "inited" key, and test whether we get an error ("not found" does not
	// count as an error).
	_, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(store.initedKey())},
			tableSortKey:      {S: aws.String(store.initedKey())},
		},
	})
	return err == nil
}

func (store *dynamoDBDataStore) Close() error {
	return nil
}

func (store *dynamoDBDataStore) prefixedNamespace(baseNamespace string) string {
	if store.prefix == "" {
		return baseNamespace
	}
	return store.prefix + ":" + baseNamespace
}

func (store *dynamoDBDataStore) namespaceForKind(kind interfaces.StoreDataKind) string {
	return store.prefixedNamespace(kind.GetName())
}

func (store *dynamoDBDataStore) initedKey() string {
	return store.prefixedNamespace("$inited")
}

func (store *dynamoDBDataStore) makeQueryForKind(kind interfaces.StoreDataKind) *dynamodb.QueryInput {
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

func (store *dynamoDBDataStore) readExistingKeys(
	newData []interfaces.StoreSerializedCollection,
) (map[namespaceAndKey]bool, error) {
	keys := make(map[namespaceAndKey]bool)
	for _, coll := range newData {
		kind := coll.Kind
		query := store.makeQueryForKind(kind)
		query.ProjectionExpression = aws.String("#namespace, #key")
		query.ExpressionAttributeNames = map[string]*string{
			"#namespace": aws.String(tablePartitionKey),
			"#key":       aws.String(tableSortKey),
		}
		err := store.client.QueryPages(query,
			func(out *dynamodb.QueryOutput, lastPage bool) bool {
				for _, i := range out.Items {
					nk := namespaceAndKey{namespace: *(i[tablePartitionKey].S), key: *(i[tableSortKey].S)}
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
func batchWriteRequests(
	client dynamodbiface.DynamoDBAPI,
	table string,
	requests []*dynamodb.WriteRequest,
) error {
	for len(requests) > 0 {
		batchSize := int(math.Min(float64(len(requests)), 25))
		batch := requests[:batchSize]
		requests = requests[batchSize:]

		_, err := client.BatchWriteItem(&dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]*dynamodb.WriteRequest{table: batch},
		})
		if err != nil {
			// COVERAGE: can't simulate this condition in unit tests because we will only get this
			// far if the initial query in Init() already succeeded, and we don't have the ability
			// to make DynamoDB fail *selectively* within a single test
			return err
		}
	}
	return nil
}

func (store *dynamoDBDataStore) decodeItem(
	av map[string]*dynamodb.AttributeValue,
) (string, interfaces.StoreSerializedItemDescriptor, bool) {
	keyValue := av[tableSortKey]
	versionValue := av[versionAttribute]
	itemJSONValue := av[itemJSONAttribute]
	if keyValue != nil && keyValue.S != nil &&
		versionValue != nil && versionValue.N != nil &&
		itemJSONValue != nil && itemJSONValue.S != nil {
		v, _ := strconv.Atoi(*versionValue.N)
		return *keyValue.S, interfaces.StoreSerializedItemDescriptor{
			Version:        v,
			SerializedItem: []byte(*itemJSONValue.S),
		}, true
	}
	return "", interfaces.StoreSerializedItemDescriptor{}, false // COVERAGE: no way to cause this in unit tests
}

func (store *dynamoDBDataStore) encodeItem(
	kind interfaces.StoreDataKind,
	key string,
	item interfaces.StoreSerializedItemDescriptor,
) map[string]*dynamodb.AttributeValue {
	return map[string]*dynamodb.AttributeValue{
		tablePartitionKey: &dynamodb.AttributeValue{S: aws.String(store.namespaceForKind(kind))},
		tableSortKey:      &dynamodb.AttributeValue{S: aws.String(key)},
		versionAttribute:  &dynamodb.AttributeValue{N: aws.String(strconv.Itoa(item.Version))},
		itemJSONAttribute: &dynamodb.AttributeValue{S: aws.String(string(item.SerializedItem))},
	}
}
