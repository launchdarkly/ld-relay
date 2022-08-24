//go:build big_segment_external_store_tests
// +build big_segment_external_store_tests

package bigsegments

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
	"github.com/stretchr/testify/require"
)

const (
	testTableName = "LD_DYNAMODB_TEST_TABLE"
	localEndpoint = "http://localhost:8000"
)

var (
	testDynamoDBConfig = aws.Config{
		Region:      aws.String("us-west-2"),
		Endpoint:    aws.String(localEndpoint),
		Credentials: credentials.NewStaticCredentials("dummy", "not", "used"),
	}
)

func TestDynamoDBGenericAll(t *testing.T) {
	require.NoError(t, createTableIfNecessary())

	t.Run("without prefix", func(t *testing.T) { testGenericAll(t, withDynamoDBStoreGeneric("")) })
	t.Run("with prefix", func(t *testing.T) { testGenericAll(t, withDynamoDBStoreGeneric("testprefix")) })
}

func (store *dynamoDBBigSegmentStore) checkSetIncludes(attribute, segmentKey, userKey string) (bool, error) {
	bigSegmentsUserDataKeyWithPrefix := dynamoDBUserDataKey(store.prefix)

	result, err := store.client.GetItem(&dynamodb.GetItemInput{
		TableName:      aws.String(store.table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]*dynamodb.AttributeValue{
			tablePartitionKey: {S: aws.String(bigSegmentsUserDataKeyWithPrefix)},
			tableSortKey:      {S: aws.String(userKey)},
		},
	})
	if err != nil || len(result.Item) == 0 {
		return false, err
	}
	item := result.Item[attribute]
	if item == nil || item.SS == nil {
		return false, nil
	}
	for _, v := range item.SS {
		if v == nil {
			continue
		}
		if *v == segmentKey {
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
			testDynamoDBConfig,
			ldlog.NewDisabledLoggers(),
		)
		require.NoError(t, err)
		require.NotNil(t, store)
		defer store.Close()
		action(store, dynamoDBMakeOperations(store))
	}
}

func createTestClient() (*dynamodb.DynamoDB, error) {
	sess, err := session.NewSession(&testDynamoDBConfig)
	if err != nil {
		return nil, err
	}

	return dynamodb.New(sess), nil
}

func clearTestData(prefix string) error {
	if prefix != "" {
		prefix += ":"
	}

	client, err := createTestClient()
	if err != nil {
		return err
	}
	var items []map[string]*dynamodb.AttributeValue

	err = client.ScanPages(&dynamodb.ScanInput{
		TableName:            aws.String(testTableName),
		ConsistentRead:       aws.Bool(true),
		ProjectionExpression: aws.String("#namespace, #key"),
		ExpressionAttributeNames: map[string]*string{
			"#namespace": aws.String(tablePartitionKey),
			"#key":       aws.String(tableSortKey),
		},
	}, func(out *dynamodb.ScanOutput, lastPage bool) bool {
		items = append(items, out.Items...)
		return !lastPage
	})
	if err != nil {
		return err
	}

	var requests []*dynamodb.WriteRequest
	for _, item := range items {
		if strings.HasPrefix(*item[tablePartitionKey].S, prefix) {
			requests = append(requests, &dynamodb.WriteRequest{
				DeleteRequest: &dynamodb.DeleteRequest{Key: item},
			})
		}
	}
	return batchWriteRequests(client, testTableName, requests)
}

func createTableIfNecessary() error {
	client, err := createTestClient()
	if err != nil {
		return err
	}
	_, err = client.DescribeTable(&dynamodb.DescribeTableInput{TableName: aws.String(testTableName)})
	if err == nil {
		return nil
	}
	if e, ok := err.(awserr.Error); !ok || e.Code() != dynamodb.ErrCodeResourceNotFoundException {
		return err
	}
	createParams := dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String(tablePartitionKey),
				AttributeType: aws.String(dynamodb.ScalarAttributeTypeS),
			},
			{
				AttributeName: aws.String(tableSortKey),
				AttributeType: aws.String(dynamodb.ScalarAttributeTypeS),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String(tablePartitionKey),
				KeyType:       aws.String(dynamodb.KeyTypeHash),
			},
			{
				AttributeName: aws.String(tableSortKey),
				KeyType:       aws.String(dynamodb.KeyTypeRange),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(1),
			WriteCapacityUnits: aws.Int64(1),
		},
		TableName: aws.String(testTableName),
	}
	_, err = client.CreateTable(&createParams)
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
			tableInfo, err := client.DescribeTable(&dynamodb.DescribeTableInput{TableName: aws.String(testTableName)})
			if err == nil && *tableInfo.Table.TableStatus == dynamodb.TableStatusActive {
				return nil
			}
		}
	}
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
