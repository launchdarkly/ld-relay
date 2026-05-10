package store

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	dynamoTablePartitionKey = "namespace"
	dynamoTableSortKey      = "key"
)

// DynamoDBInitChecker implements StoreInitChecker by directly querying DynamoDB
// for the $inited item, bypassing the SDK's caching layer.
type DynamoDBInitChecker struct {
	client    *dynamodb.Client
	tableName string
	prefix    string
}

// NewDynamoDBInitChecker creates a checker that connects to DynamoDB.
// The tableName and prefix should match those used by the SDK store.
// If endpoint is non-nil, it overrides the default AWS endpoint (for local testing).
func NewDynamoDBInitChecker(tableName string, prefix string, endpoint *string) (*DynamoDBInitChecker, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	var options []func(*dynamodb.Options)
	if endpoint != nil {
		options = append(options, func(o *dynamodb.Options) {
			o.BaseEndpoint = endpoint
		})
	}
	client := dynamodb.NewFromConfig(cfg, options...)
	return &DynamoDBInitChecker{
		client:    client,
		tableName: tableName,
		prefix:    prefix,
	}, nil
}

func (d *DynamoDBInitChecker) initedKey() string {
	if d.prefix == "" {
		return "$inited"
	}
	return d.prefix + ":$inited"
}

func attrValueStr(s string) *types.AttributeValueMemberS {
	return &types.AttributeValueMemberS{Value: s}
}

// CheckInitialized checks if the $inited item exists in the DynamoDB table.
func (d *DynamoDBInitChecker) CheckInitialized() (available bool, initialized bool, err error) {
	key := d.initedKey()
	result, err := d.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			dynamoTablePartitionKey: attrValueStr(key),
			dynamoTableSortKey:      attrValueStr(key),
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return false, false, err
	}
	return true, len(result.Item) > 0, nil
}

// Close is a no-op for DynamoDB (the client doesn't need explicit cleanup).
func (d *DynamoDBInitChecker) Close() error {
	return nil
}
