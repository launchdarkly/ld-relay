package autoconfigcache

import (
	"context"
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	dynamoDBNamespace = "ld:autoconfig"
	dynamoDBValueAttr = "value"
)

type dynamoDBStore struct {
	client   *dynamodb.Client
	table    string
	cacheKey string
	encKey   []byte
	loggers  ldlog.Loggers
}

func newDynamoDBStore(dbConfig config.DynamoDBConfig, cacheKey string, encKey []byte, loggers ldlog.Loggers) (Store, error) {
	if dbConfig.TableName == "" {
		return nil, fmt.Errorf("DynamoDB table name required for AutoConfig cache")
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	opts := []func(*dynamodb.Options){}
	if dbConfig.URL.IsDefined() {
		opts = append(opts, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(dbConfig.URL.String())
		})
	}
	client := dynamodb.NewFromConfig(cfg, opts...)
	return &dynamoDBStore{
		client:   client,
		table:    dbConfig.TableName,
		cacheKey: cacheKey,
		encKey:   encKey,
		loggers:  loggers,
	}, nil
}

func (s *dynamoDBStore) Get(ctx context.Context) ([]byte, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key: map[string]types.AttributeValue{
			"namespace": &types.AttributeValueMemberS{Value: dynamoDBNamespace},
			"key":       &types.AttributeValueMemberS{Value: s.cacheKey},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, nil
	}
	v, ok := out.Item[dynamoDBValueAttr].(*types.AttributeValueMemberB)
	if !ok || v == nil {
		return nil, nil
	}
	return decrypt(v.Value, s.encKey)
}

func (s *dynamoDBStore) Set(ctx context.Context, value []byte) error {
	enc, err := encrypt(value, s.encKey)
	if err != nil {
		return fmt.Errorf("encrypt autoconfig cache: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item: map[string]types.AttributeValue{
			"namespace": &types.AttributeValueMemberS{Value: dynamoDBNamespace},
			"key":       &types.AttributeValueMemberS{Value: s.cacheKey},
			dynamoDBValueAttr: &types.AttributeValueMemberB{Value: enc},
		},
	})
	return err
}

func (s *dynamoDBStore) Close() error {
	return nil
}
