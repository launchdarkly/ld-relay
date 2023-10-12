//go:build big_segment_external_store_tests
// +build big_segment_external_store_tests

package bigsegments

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"
)

const (
	testTableName = "LD_DYNAMODB_TEST_TABLE"
	localEndpoint = "http://localhost:8000"
)

func TestDynamoDBGenericAll(t *testing.T) {
	require.NoError(t, createTableIfNecessary())

	t.Run("without prefix", func(t *testing.T) { testGenericAll(t, withDynamoDBStoreGeneric("")) })
	t.Run("with prefix", func(t *testing.T) { testGenericAll(t, withDynamoDBStoreGeneric("testprefix")) })
}

func (store *dynamoDBBigSegmentStore) checkSetIncludes(attribute, segmentKey, userKey string) (bool, error) {
	bigSegmentsUserDataKeyWithPrefix := dynamoDBUserDataKey(store.prefix)

	result, err := store.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			tablePartitionKey: attrValueOfString(bigSegmentsUserDataKeyWithPrefix),
			tableSortKey:      attrValueOfString(userKey),
		},
	})
	if err != nil || len(result.Item) == 0 {
		return false, err
	}
	item := result.Item[attribute]
	ssValue, ok := item.(*types.AttributeValueMemberSS)
	if !ok {
		return false, nil
	}
	for _, v := range ssValue.Value {
		if v == segmentKey {
			return true, nil
		}
	}
	return false, nil
}

func dynamoDBMakeOperations(store *dynamoDBBigSegmentStore) bigSegmentOperations {
	return bigSegmentOperations{
		isUserIncluded: func(segmentKey string, userKey string) (bool, error) {
			return store.checkSetIncludes(dynamoDBIncludedAttr, segmentKey, userKey)
		},
		isUserExcluded: func(segmentKey string, userKey string) (bool, error) {
			return store.checkSetIncludes(dynamoDBExcludedAttr, segmentKey, userKey)
		},
	}
}

func withDynamoDBStoreGeneric(prefix string) func(*testing.T, func(BigSegmentStore, bigSegmentOperations)) {
	return func(t *testing.T, action func(BigSegmentStore, bigSegmentOperations)) {
		require.NoError(t, clearTestData(prefix))
		store, err := newDynamoDBBigSegmentStore(
			config.DynamoDBConfig{TableName: testTableName},
			config.EnvConfig{Prefix: prefix},
			[]func(*dynamodb.Options){setTestDynamoDBOptions},
			ldlog.NewDisabledLoggers(),
		)
		require.NoError(t, err)
		require.NotNil(t, store)
		defer store.Close()
		action(store, dynamoDBMakeOperations(store))
	}
}

func createTestClient() *dynamodb.Client {
	return dynamodb.New(dynamodb.Options{}, setTestDynamoDBOptions)
}

func clearTestData(prefix string) error {
	if prefix != "" {
		prefix += ":"
	}

	client := createTestClient()
	var items []map[string]types.AttributeValue

	scanInput := dynamodb.ScanInput{
		TableName:            aws.String(testTableName),
		ConsistentRead:       aws.Bool(true),
		ProjectionExpression: aws.String("#namespace, #key"),
		ExpressionAttributeNames: map[string]string{
			"#namespace": tablePartitionKey,
			"#key":       tableSortKey,
		},
	}
	for {
		out, err := client.Scan(context.Background(), &scanInput)
		if err != nil {
			return err
		}
		items = append(items, out.Items...)
		if out.LastEvaluatedKey == nil {
			break
		}
		scanInput.ExclusiveStartKey = out.LastEvaluatedKey
	}

	var requests []types.WriteRequest
	for _, item := range items {
		if strings.HasPrefix(attrValueToString(item[tablePartitionKey]), prefix) {
			requests = append(requests, types.WriteRequest{
				DeleteRequest: &types.DeleteRequest{Key: item},
			})
		}
	}
	return batchWriteRequests(context.Background(), client, testTableName, requests)
}

func createTableIfNecessary() error {
	client := createTestClient()
	_, err := client.DescribeTable(context.Background(),
		&dynamodb.DescribeTableInput{TableName: aws.String(testTableName)})
	if err == nil {
		return nil
	}
	var resNotFoundErr *types.ResourceNotFoundException
	if !errors.As(err, &resNotFoundErr) {
		return err
	}
	createParams := dynamodb.CreateTableInput{
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String(tablePartitionKey),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String(tableSortKey),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String(tablePartitionKey),
				KeyType:       types.KeyTypeHash,
			},
			{
				AttributeName: aws.String(tableSortKey),
				KeyType:       types.KeyTypeRange,
			},
		},
		ProvisionedThroughput: &types.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(1),
			WriteCapacityUnits: aws.Int64(1),
		},
		TableName: aws.String(testTableName),
	}
	_, err = client.CreateTable(context.Background(), &createParams)
	if err != nil {
		return err
	}
	// When DynamoDB creates a table, it may not be ready to use immediately
	deadline := time.After(10 * time.Second)
	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for new table to be ready")
		case <-retry.C:
			tableInfo, err := client.DescribeTable(context.Background(),
				&dynamodb.DescribeTableInput{TableName: aws.String(testTableName)})
			if err == nil && tableInfo.Table.TableStatus == types.TableStatusActive {
				return nil
			}
		}
	}
}

// batchWriteRequests executes a list of write requests (PutItem or DeleteItem)
// in batches of 25, which is the maximum BatchWriteItem can handle.
func batchWriteRequests(
	context context.Context,
	client *dynamodb.Client,
	table string,
	requests []types.WriteRequest,
) error {
	for len(requests) > 0 {
		batchSize := int(math.Min(float64(len(requests)), 25))
		batch := requests[:batchSize]
		requests = requests[batchSize:]

		_, err := client.BatchWriteItem(context, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{table: batch},
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

func attrValueToString(value types.AttributeValue) string {
	switch v := value.(type) {
	case *types.AttributeValueMemberS:
		return v.Value
	case *types.AttributeValueMemberN:
		return v.Value
	default:
		return ""
	}
}

func setTestDynamoDBOptions(o *dynamodb.Options) {
	o.Region = "us-west-2"
	o.EndpointResolver = dynamodb.EndpointResolverFromURL(localEndpoint)
	o.Credentials = credentials.NewStaticCredentialsProvider("dummy", "not", "used")
}
