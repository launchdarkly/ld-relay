package bigsegments

import (
	"context"
	"errors"
	"strconv"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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
	client        *dynamodb.Client
	context       context.Context
	cancelContext context.CancelFunc
	loggers       ldlog.Loggers
	table         string
	prefix        string
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
	optFns []func(*dynamodb.Options),
	loggers ldlog.Loggers,
) (*dynamoDBBigSegmentStore, error) {
	config, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}

	endpoint, table, prefix := sdks.GetDynamoDBBasicProperties(dbConfig, envConfig)
	if endpoint != nil {
		optFns = append(optFns, func(o *dynamodb.Options) {
			o.EndpointResolver = dynamodb.EndpointResolverFromURL(*endpoint)
		})
	}

	client := dynamodb.NewFromConfig(config, optFns...)
	context, cancelContext := context.WithCancel(context.Background())

	store := dynamoDBBigSegmentStore{
		table:         table,
		loggers:       loggers,
		prefix:        prefix,
		client:        client,
		context:       context,
		cancelContext: cancelContext,
	}

	store.loggers.SetPrefix("DynamoDBBigSegmentStore:")
	store.loggers.Infof(`Using DynamoDB table %s`, store.table)

	return &store, nil
}

func (store *dynamoDBBigSegmentStore) makeTransactionItem(updateExpression, attribute, segmentID, userKey string) types.TransactWriteItem {
	return types.TransactWriteItem{
		Update: &types.Update{
			TableName:        aws.String(store.table),
			UpdateExpression: aws.String(updateExpression),
			ExpressionAttributeNames: map[string]string{
				"#0": attribute,
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":0": &types.AttributeValueMemberSS{Value: []string{segmentID}},
			},
			Key: map[string]types.AttributeValue{
				tablePartitionKey: attrValueOfString(dynamoDBUserDataKey(store.prefix)),
				tableSortKey:      attrValueOfString(userKey),
			},
		},
	}
}

func makeCursorUpdateCondition(previousVersion string) (string, map[string]string, map[string]types.AttributeValue) {
	names := map[string]string{"#0": dynamoDBCursorAttr}
	if previousVersion == "" {
		return "attribute_not_exists(#0)", names, nil
	}
	return "#0 = :0", names, map[string]types.AttributeValue{
		":0": attrValueOfString(previousVersion),
	}
}

func (store *dynamoDBBigSegmentStore) applyPatch(patch bigSegmentPatch) (bool, error) {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)

	txConditionExpression, txExprAttrNames, txExprAttrValues := makeCursorUpdateCondition(patch.PreviousVersion)

	conditionCheckItem := types.TransactWriteItem{
		ConditionCheck: &types.ConditionCheck{
			ConditionExpression:       aws.String(txConditionExpression),
			TableName:                 aws.String(store.table),
			ExpressionAttributeNames:  txExprAttrNames,
			ExpressionAttributeValues: txExprAttrValues,
			Key: map[string]types.AttributeValue{
				tablePartitionKey: attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
				tableSortKey:      attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
			},
			ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureNone,
		},
	}

	totalItems := len(patch.Changes.Included.Add) + len(patch.Changes.Included.Remove) +
		len(patch.Changes.Excluded.Add) + len(patch.Changes.Excluded.Remove)
	transactionItems := make([]types.TransactWriteItem, 0, totalItems)

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

	transactionBatch := make([]types.TransactWriteItem, 0, dynamoTransactionMaxItems)

	for batchStart := 0; batchStart < len(transactionItems); batchStart += dynamoTransactionMaxItems - 1 {
		batchEnd := batchStart + dynamoTransactionMaxItems - 1
		if batchEnd > len(transactionItems) {
			batchEnd = len(transactionItems)
		}
		transactionBatch = append(transactionBatch, conditionCheckItem)
		transactionBatch = append(transactionBatch, transactionItems[batchStart:batchEnd]...)
		_, err := store.client.TransactWriteItems(store.context, &dynamodb.TransactWriteItemsInput{
			TransactItems: transactionBatch,
		})
		if err != nil {
			// DynamoDB doesn't seem to provide a more convenient programmatic way to distinguish
			// "transaction was cancelled due to the condition check" from other errors here; we
			// need to go to this trouble because we want the synchronizer to be able to log an
			// out-of-order update in a clear way that doesn't look like a random database error.
			var canceledErr *types.TransactionCanceledException
			if errors.As(err, &canceledErr) {
				for _, reason := range canceledErr.CancellationReasons {
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
		updateExprAttrValues = map[string]types.AttributeValue{}
	}
	updateExprAttrValues[":1"] = &types.AttributeValueMemberS{Value: patch.Version}
	updateCursorInput := dynamodb.UpdateItemInput{
		ConditionExpression:       aws.String(updateConditionExpression),
		TableName:                 aws.String(store.table),
		ExpressionAttributeNames:  updateExprAttrNames,
		ExpressionAttributeValues: updateExprAttrValues,
		Key: map[string]types.AttributeValue{
			tablePartitionKey: attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
			tableSortKey:      attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
		},
		UpdateExpression: aws.String("SET #0 = :1"),
	}

	_, err := store.client.UpdateItem(store.context, &updateCursorInput)
	if err == nil {
		return true, nil
	}
	return false, err
}

func (store *dynamoDBBigSegmentStore) getCursor() (string, error) {
	metadataKey := dynamoDBMetadataKey(store.prefix)
	result, err := store.client.GetItem(store.context, &dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		ExpressionAttributeNames: map[string]string{
			"#0": dynamoDBCursorAttr,
		},
		Key: map[string]types.AttributeValue{
			tablePartitionKey: attrValueOfString(metadataKey),
			tableSortKey:      attrValueOfString(metadataKey),
		},
		ProjectionExpression: aws.String("#0"),
	})
	if err != nil || len(result.Item) == 0 {
		return "", err
	}
	item := result.Item[dynamoDBCursorAttr]
	if sValue, ok := item.(*types.AttributeValueMemberS); ok {
		return sValue.Value, nil
	}
	return "", nil
}

func (store *dynamoDBBigSegmentStore) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)
	unixMilliseconds := strconv.FormatUint(uint64(synchronizedOn), 10)
	_, err := store.client.UpdateItem(store.context, &dynamodb.UpdateItemInput{
		TableName: aws.String(store.table),
		Key: map[string]types.AttributeValue{
			tablePartitionKey: attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
			tableSortKey:      attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
		},
		UpdateExpression:         aws.String("SET #0 = :0"),
		ExpressionAttributeNames: map[string]string{"#0": dynamoDBSyncTimeAttr},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":0": &types.AttributeValueMemberN{Value: unixMilliseconds},
		},
	})
	return err
}

func (store *dynamoDBBigSegmentStore) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	bigSegmentsMetadataKeyWithPrefix := dynamoDBMetadataKey(store.prefix)
	result, err := store.client.GetItem(store.context, &dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			tablePartitionKey: attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
			tableSortKey:      attrValueOfString(bigSegmentsMetadataKeyWithPrefix),
		},
		ProjectionExpression: aws.String(dynamoDBSyncTimeAttr),
	})
	if err != nil || len(result.Item) == 0 {
		return 0, err
	}
	item := result.Item[dynamoDBSyncTimeAttr]
	if nValue, ok := item.(*types.AttributeValueMemberN); ok {
		value, err := strconv.ParseUint(nValue.Value, 10, 64)
		if err != nil {
			return 0, err
		}
		return ldtime.UnixMillisecondTime(value), nil
	}
	return 0, nil
}

func (store *dynamoDBBigSegmentStore) Close() error {
	return nil
}

func attrValueOfString(value string) types.AttributeValue {
	return &types.AttributeValueMemberS{Value: value}
}
