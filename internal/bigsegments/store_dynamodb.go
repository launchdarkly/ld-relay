package bigsegments

import (
	"strconv"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/sdks"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
)

const (
	tablePartitionKey         = "namespace"
	tableSortKey              = "key"
	dynamoDBCursorAttr        = "lastVersion"
	dynamoDBIncludedAttr      = "included"
	dynamoDBExcludedAttr      = "excluded"
	dynamoDBSyncTimeAttr      = "synchronizedOn"
	updateExpressionAdd       = "ADD #0 :0"
	updateExpressionRemove    = "DELETE #0 :0"
	dynamoTransactionMaxItems = 25
)

// dynamoDBBigSegmentStore implements BigSegmentStore for DynamoDB.
type dynamoDBBigSegmentStore struct {
	client  dynamodbiface.DynamoDBAPI
	loggers ldlog.Loggers
	table   string
	prefix  string
}

func dynamoDBMetadataKey(prefix string) string {
	return dynamoDBPrefixedKey(prefix, "big_segments_metadata")
}

func dynamoDBUserDataKey(prefix string) string {
	return dynamoDBPrefixedKey(prefix, "big_segments_user")
}

func dynamoDBPrefixedKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + ":" + key
}

func newDynamoDBBigSegmentStore(
	dbConfig config.DynamoDBConfig,
	envConfig config.EnvConfig,
	config aws.Config,
	loggers ldlog.Loggers,
) (*dynamoDBBigSegmentStore, error) {
	endpoint, table, prefix := sdks.GetDynamoDBBasicProperties(dbConfig, envConfig)

	if endpoint != nil {
		config.Endpoint = endpoint
	}

	sess, err := session.NewSession(&config)
	if err != nil {
		return nil, err
	}

	store := dynamoDBBigSegmentStore{
		table:   table,
		loggers: loggers,
		prefix:  prefix,
		client:  dynamodb.New(sess),
	}

	store.loggers.SetPrefix("DynamoDBBigSegmentStore:")
	store.loggers.Infof(`Using DynamoDB table %s`, store.table)

	return &store, nil
}

func (store *dynamoDBBigSegmentStore) makeTransactionItem(updateExpression, attribute, segmentID, userKey string) *dynamodb.TransactWriteItem {
	return &dynamodb.TransactWriteItem{
		Update: &dynamodb.Update{
			TableName:        aws.String(store.table),
			UpdateExpression: aws.String(updateExpression),
			ExpressionAttributeNames: map[string]*string{
				"#0": aws.String(attribute),
			},
			ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
				":0": {SS: []*string{aws.String(segmentID)}},
			},
			Key: map[string]*dynamodb.AttributeValue{
				tablePartitionKey: {S: aws.String(dynamoDBUserDataKey(store.prefix))},
				tableSortKey:      {S: aws.String(userKey)},
			},
		},
	}
}

func makeCursorUpdateCondition(previousVersion string) (string, map[string]*string, map[string]*dynamodb.AttributeValue) {
	names := map[string]*string{"#0": aws.String(dynamoDBCursorAttr)}
	if previousVersion == "" {
		return "attribute_not_exists(#0)", names, nil
	}
	return "#0 = :0", names, map[string]*dynamodb.AttributeValue{
		":0": {S: aws.String(previousVersion)},
	}
}

func (store *dynamoDBBigSegmentStore) applyPatch(patch bigSegmentPatch) (bool, error) {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)

	txConditionExpression, txExprAttrNames, txExprAttrValues := makeCursorUpdateCondition(patch.PreviousVersion)

	conditionCheckItem := &dynamodb.TransactWriteItem{
		ConditionCheck: &dynamodb.ConditionCheck{
			ConditionExpression:       aws.String(txConditionExpression),
			TableName:                 aws.String(store.table),
			ExpressionAttributeNames:  txExprAttrNames,
			ExpressionAttributeValues: txExprAttrValues,
			Key: map[string]*dynamodb.AttributeValue{
				tablePartitionKey: {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
				tableSortKey:      {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			},
			ReturnValuesOnConditionCheckFailure: aws.String(dynamodb.ReturnValuesOnConditionCheckFailureNone),
		},
	}

	totalItems := len(patch.Changes.Included.Add) + len(patch.Changes.Included.Remove) +
		len(patch.Changes.Excluded.Add) + len(patch.Changes.Excluded.Remove)
	transactionItems := make([]*dynamodb.TransactWriteItem, 0, totalItems)

	for _, user := range patch.Changes.Included.Add {
		item := store.makeTransactionItem(updateExpressionAdd, dynamoDBIncludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Excluded.Add {
		item := store.makeTransactionItem(updateExpressionAdd, dynamoDBExcludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Included.Remove {
		item := store.makeTransactionItem(updateExpressionRemove, dynamoDBIncludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Excluded.Remove {
		item := store.makeTransactionItem(updateExpressionRemove, dynamoDBExcludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	transactionBatch := make([]*dynamodb.TransactWriteItem, 0, dynamoTransactionMaxItems)

	for batchStart := 0; batchStart < len(transactionItems); batchStart += dynamoTransactionMaxItems - 1 {
		batchEnd := batchStart + dynamoTransactionMaxItems - 1
		if batchEnd > len(transactionItems) {
			batchEnd = len(transactionItems)
		}
		transactionBatch = append(transactionBatch, conditionCheckItem)
		transactionBatch = append(transactionBatch, transactionItems[batchStart:batchEnd]...)
		_, err := store.client.TransactWriteItems(&dynamodb.TransactWriteItemsInput{
			TransactItems: transactionBatch,
		})
		if err != nil {
			// DynamoDB doesn't seem to provide a more convenient programmatic way to distinguish
			// "transaction was cancelled due to the condition check" from other errors here; we
			// need to go to this trouble because we want the synchronizer to be able to log an
			// out-of-order update in a clear way that doesn't look like a random database error.
			if tce, ok := err.(*dynamodb.TransactionCanceledException); ok {
				for _, reason := range tce.CancellationReasons {
					if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
						return false, nil
					}
				}
			}
			return false, err
		}
		transactionBatch = transactionBatch[:0]
	}

	updateConditionExpression, updateExprAttrNames, updateExprAttrValues := makeCursorUpdateCondition(patch.PreviousVersion)
	if updateExprAttrValues == nil {
		updateExprAttrValues = map[string]*dynamodb.AttributeValue{}
	}
	updateExprAttrValues[":1"] = &dynamodb.AttributeValue{
		S: aws.String(patch.Version),
	}
	updateCursorInput := dynamodb.UpdateItemInput{
		ConditionExpression:       aws.String(updateConditionExpression),
		TableName:                 aws.String(store.table),
		ExpressionAttributeNames:  updateExprAttrNames,
		ExpressionAttributeValues: updateExprAttrValues,
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:      {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
		},
		UpdateExpression: aws.String("SET #0 = :1"),
	}

	_, err := store.client.UpdateItem(&updateCursorInput)
	if err == nil {
		return true, nil
	}
	return false, err
}

func (store *dynamoDBBigSegmentStore) getCursor() (string, error) {
	metadataKey := dynamoDBMetadataKey(store.prefix)
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		ExpressionAttributeNames: map[string]*string{
			"#0": aws.String(dynamoDBCursorAttr),
		},
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(metadataKey)},
			tableSortKey:      {S: aws.String(metadataKey)},
		},
		ProjectionExpression: aws.String("#0"),
	})
	if err != nil || len(result.Item) == 0 {
		return "", err
	}
	item := result.Item[dynamoDBCursorAttr]
	if item == nil || item.S == nil {
		return "", nil
	}
	return *item.S, nil
}

func (store *dynamoDBBigSegmentStore) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)
	unixMilliseconds := strconv.FormatUint(uint64(synchronizedOn), 10)
	_, err := store.client.UpdateItem(&dynamodb.UpdateItemInput{
		TableName: aws.String(store.table),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:      {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
		},
		UpdateExpression:         aws.String("SET #0 = :0"),
		ExpressionAttributeNames: map[string]*string{"#0": aws.String(dynamoDBSyncTimeAttr)},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":0": {
				N: aws.String(unixMilliseconds),
			},
		},
	})
	return err
}

func (store *dynamoDBBigSegmentStore) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:      {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
		},
		ProjectionExpression: aws.String(dynamoDBSyncTimeAttr),
	})
	if err != nil || len(result.Item) == 0 {
		return 0, err
	}
	item := result.Item[dynamoDBSyncTimeAttr]
	if item == nil || item.N == nil {
		return 0, nil
	}
	value, err := strconv.Atoi(*item.N)
	if err != nil {
		return 0, nil
	}
	return ldtime.UnixMillisecondTime(value), nil
}

func (store *dynamoDBBigSegmentStore) Close() error {
	return nil
}
