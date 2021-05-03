package bigsegments

import (
	"strconv"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
)

const (
	tablePartitionKey         = "namespace"
	tableSortKey              = "key"
	bigSegmentsMetadataKey    = "big_segments_metadata"
	bigSegmentsUserDataKey    = "big_segments_user"
	bigSegmentsCursorAttr     = "cursor"
	bigSegmentsIncludedAttr   = "included"
	bigSegmentsExcludedAttr   = "excluded"
	bigSegmentsSyncTimeAttr   = "synchronizedOn"
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

func newDynamoDBBigSegmentStore(
	url ct.OptURLAbsolute,
	config aws.Config,
	loggers ldlog.Loggers,
	table string,
	prefix string,
) (*dynamoDBBigSegmentStore, error) {
	if url.IsDefined() {
		config.Endpoint = aws.String(url.String())
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
				tablePartitionKey: {S: aws.String(store.addPrefix(bigSegmentsUserDataKey))},
				tableSortKey:      {S: aws.String(userKey)},
			},
		},
	}
}

func (store *dynamoDBBigSegmentStore) applyPatch(patch bigSegmentPatch) error {
	bigSegmentsMetadataKeyWithPrefix := store.addPrefix(bigSegmentsMetadataKey)

	var conditionExpression *string
	var expressionAttributeValues map[string]*dynamodb.AttributeValue
	if patch.PreviousVersion == "" {
		conditionExpression = aws.String("attribute_not_exists(#0)")
	} else {
		conditionExpression = aws.String("#0 = :0")
		expressionAttributeValues = map[string]*dynamodb.AttributeValue{
			":0": {S: aws.String(patch.PreviousVersion)},
		}
	}

	conditionCheckItem := &dynamodb.TransactWriteItem{
		ConditionCheck: &dynamodb.ConditionCheck{
			ConditionExpression:       conditionExpression,
			ExpressionAttributeValues: expressionAttributeValues,
			TableName:                 aws.String(store.table),
			ExpressionAttributeNames: map[string]*string{
				"#0": aws.String(bigSegmentsCursorAttr),
			},
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
		item := store.makeTransactionItem(updateExpressionAdd, bigSegmentsIncludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Excluded.Add {
		item := store.makeTransactionItem(updateExpressionAdd, bigSegmentsExcludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Included.Remove {
		item := store.makeTransactionItem(updateExpressionRemove, bigSegmentsIncludedAttr, patch.SegmentID, user)
		transactionItems = append(transactionItems, item)
	}

	for _, user := range patch.Changes.Excluded.Remove {
		item := store.makeTransactionItem(updateExpressionRemove, bigSegmentsExcludedAttr, patch.SegmentID, user)
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
			return err
		}
		transactionBatch = transactionBatch[:0]
	}

	putCursorInput := dynamodb.PutItemInput{
		ConditionExpression:       conditionExpression,
		ExpressionAttributeValues: expressionAttributeValues,
		TableName:                 aws.String(store.table),
		ExpressionAttributeNames: map[string]*string{
			"#0": aws.String(bigSegmentsCursorAttr),
		},
		Item: map[string]*dynamodb.AttributeValue{
			tablePartitionKey:     {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:          {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			bigSegmentsCursorAttr: {S: aws.String(patch.Version)},
		},
	}

	_, err := store.client.PutItem(&putCursorInput)
	return err
}

func (store *dynamoDBBigSegmentStore) getCursor() (string, error) {
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		ExpressionAttributeNames: map[string]*string{
			"#0": aws.String(bigSegmentsCursorAttr),
		},
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsMetadataKey)},
			tableSortKey:      {S: aws.String(bigSegmentsMetadataKey)},
		},
		ProjectionExpression: aws.String("#0"),
	})
	if err != nil || len(result.Item) == 0 {
		return "", err
	}
	item := result.Item[bigSegmentsCursorAttr]
	if item == nil || item.S == nil {
		return "", nil
	}
	return *item.S, nil
}

func (store *dynamoDBBigSegmentStore) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	bigSegmentsMetadataKeyWithPrefix := store.addPrefix(bigSegmentsMetadataKey)
	unixMilliseconds := strconv.FormatUint(uint64(synchronizedOn), 10)
	_, err := store.client.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(store.table),
		Item: map[string]*dynamodb.AttributeValue{
			tablePartitionKey:       {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:            {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			bigSegmentsSyncTimeAttr: {N: aws.String(unixMilliseconds)},
		},
	})
	return err
}

func (store *dynamoDBBigSegmentStore) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	bigSegmentsMetadataKeyWithPrefix := store.addPrefix(bigSegmentsMetadataKey)
	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
			tableSortKey:      {S: aws.String(bigSegmentsMetadataKeyWithPrefix)},
		},
		ProjectionExpression: aws.String(bigSegmentsSyncTimeAttr),
	})
	if err != nil || len(result.Item) == 0 {
		return 0, err
	}
	item := result.Item[bigSegmentsSyncTimeAttr]
	if item == nil || item.N == nil {
		return 0, nil
	}
	value, err := strconv.Atoi(*item.N)
	if err != nil {
		return 0, nil
	}
	return ldtime.UnixMillisecondTime(value), nil
}

func (store *dynamoDBBigSegmentStore) addPrefix(key string) string {
	if store.prefix == "" {
		return key
	}
	return store.prefix + ":" + key
}

func (store *dynamoDBBigSegmentStore) Close() error {
	return nil
}
