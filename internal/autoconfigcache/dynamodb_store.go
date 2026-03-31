package autoconfigcache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

const (
	dynamoDBPartitionKey = "namespace"
	dynamoDBSortKey      = "key"
	dynamoDBItemAttr     = "item"
	dynamoDBMaxItemSize  = 400000
	dynamoDBMaxBatchSize = 25

	dynamoDBEnvPrefix    = "env:"
	dynamoDBFilterPrefix = "filter:"
)

type dynamoDBStore struct {
	client    *dynamodb.Client
	table     string
	namespace string
	encKey    []byte
	loggers   ldlog.Loggers
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
		client:    client,
		table:     dbConfig.TableName,
		namespace: "ld:autoconfig:" + cacheKey,
		encKey:    encKey,
		loggers:   loggers,
	}, nil
}

func (s *dynamoDBStore) GetAll(ctx context.Context) (*autoconfig.PutContent, error) {
	content := &autoconfig.PutContent{
		Environments: make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		Filters:      make(map[config.FilterID]envfactory.FilterRep),
	}

	var exclusiveStartKey map[string]types.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			ConsistentRead:         aws.Bool(true),
			ExclusiveStartKey:      exclusiveStartKey,
			KeyConditionExpression: aws.String("#ns = :ns"),
			ExpressionAttributeNames: map[string]string{
				"#ns": dynamoDBPartitionKey,
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":ns": &types.AttributeValueMemberS{Value: s.namespace},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("AutoConfig cache DynamoDB query: %w", err)
		}

		for _, item := range out.Items {
			sortKey := ""
			if sk, ok := item[dynamoDBSortKey].(*types.AttributeValueMemberS); ok {
				sortKey = sk.Value
			}
			itemAttr, ok := item[dynamoDBItemAttr].(*types.AttributeValueMemberB)
			if !ok || itemAttr == nil {
				continue
			}
			plaintext, err := decrypt(itemAttr.Value, s.encKey)
			if err != nil {
				s.loggers.Warnf("AutoConfig cache: failed to decrypt item %q: %v", sortKey, err)
				continue
			}

			if strings.HasPrefix(sortKey, dynamoDBEnvPrefix) {
				envID := config.EnvironmentID(strings.TrimPrefix(sortKey, dynamoDBEnvPrefix))
				var rep envfactory.EnvironmentRep
				if err := json.Unmarshal(plaintext, &rep); err != nil {
					s.loggers.Warnf("AutoConfig cache: failed to unmarshal env %q: %v", envID, err)
					continue
				}
				content.Environments[envID] = rep
			} else if strings.HasPrefix(sortKey, dynamoDBFilterPrefix) {
				filterID := config.FilterID(strings.TrimPrefix(sortKey, dynamoDBFilterPrefix))
				var rep envfactory.FilterRep
				if err := json.Unmarshal(plaintext, &rep); err != nil {
					s.loggers.Warnf("AutoConfig cache: failed to unmarshal filter %q: %v", filterID, err)
					continue
				}
				content.Filters[filterID] = rep
			}
		}

		if out.LastEvaluatedKey == nil {
			break
		}
		exclusiveStartKey = out.LastEvaluatedKey
	}

	if len(content.Environments) == 0 && len(content.Filters) == 0 {
		return nil, nil
	}
	return content, nil
}

func (s *dynamoDBStore) SetAll(ctx context.Context, content autoconfig.PutContent) error {
	// Step 1: Query existing sort keys so we can delete stale items.
	existingKeys := make(map[string]bool)
	var exclusiveStartKey map[string]types.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			ConsistentRead:         aws.Bool(true),
			ExclusiveStartKey:      exclusiveStartKey,
			KeyConditionExpression: aws.String("#ns = :ns"),
			ProjectionExpression:   aws.String("#k"),
			ExpressionAttributeNames: map[string]string{
				"#ns": dynamoDBPartitionKey,
				"#k":  dynamoDBSortKey,
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":ns": &types.AttributeValueMemberS{Value: s.namespace},
			},
		})
		if err != nil {
			return fmt.Errorf("AutoConfig cache DynamoDB query existing: %w", err)
		}
		for _, item := range out.Items {
			if sk, ok := item[dynamoDBSortKey].(*types.AttributeValueMemberS); ok {
				existingKeys[sk.Value] = true
			}
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		exclusiveStartKey = out.LastEvaluatedKey
	}

	// Step 2: Build write requests for new items.
	var writeRequests []types.WriteRequest
	newKeys := make(map[string]bool)

	for id, rep := range content.Environments {
		if id != rep.EnvID {
			continue
		}
		sortKey := dynamoDBEnvPrefix + string(id)
		newKeys[sortKey] = true
		item, err := s.buildItem(sortKey, rep)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to build env item %q: %v", id, err)
			continue
		}
		if !s.checkSizeLimit(item, sortKey) {
			continue
		}
		writeRequests = append(writeRequests, types.WriteRequest{
			PutRequest: &types.PutRequest{Item: item},
		})
	}

	for id, rep := range content.Filters {
		sortKey := dynamoDBFilterPrefix + string(id)
		newKeys[sortKey] = true
		item, err := s.buildItem(sortKey, rep)
		if err != nil {
			s.loggers.Warnf("AutoConfig cache: failed to build filter item %q: %v", id, err)
			continue
		}
		if !s.checkSizeLimit(item, sortKey) {
			continue
		}
		writeRequests = append(writeRequests, types.WriteRequest{
			PutRequest: &types.PutRequest{Item: item},
		})
	}

	// Step 3: Build delete requests for stale items.
	for key := range existingKeys {
		if !newKeys[key] {
			writeRequests = append(writeRequests, types.WriteRequest{
				DeleteRequest: &types.DeleteRequest{
					Key: map[string]types.AttributeValue{
						dynamoDBPartitionKey: &types.AttributeValueMemberS{Value: s.namespace},
						dynamoDBSortKey:      &types.AttributeValueMemberS{Value: key},
					},
				},
			})
		}
	}

	// Step 4: Batch write in chunks of 25.
	return s.batchWrite(ctx, writeRequests)
}

func (s *dynamoDBStore) buildItem(sortKey string, value interface{}) (map[string]types.AttributeValue, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	enc, err := encrypt(raw, s.encKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	return map[string]types.AttributeValue{
		dynamoDBPartitionKey: &types.AttributeValueMemberS{Value: s.namespace},
		dynamoDBSortKey:      &types.AttributeValueMemberS{Value: sortKey},
		dynamoDBItemAttr:     &types.AttributeValueMemberB{Value: enc},
	}, nil
}

func (s *dynamoDBStore) checkSizeLimit(item map[string]types.AttributeValue, sortKey string) bool {
	size := 100 // fixed overhead for index data
	for key, value := range item {
		size += len(key)
		switch v := value.(type) {
		case *types.AttributeValueMemberS:
			size += len(v.Value)
		case *types.AttributeValueMemberB:
			size += len(v.Value)
		}
	}
	if size <= dynamoDBMaxItemSize {
		return true
	}
	s.loggers.Errorf("AutoConfig cache item %q in %q was too large to store in DynamoDB and was dropped", sortKey, s.namespace)
	return false
}

func (s *dynamoDBStore) batchWrite(ctx context.Context, requests []types.WriteRequest) error {
	for i := 0; i < len(requests); i += dynamoDBMaxBatchSize {
		end := i + dynamoDBMaxBatchSize
		if end > len(requests) {
			end = len(requests)
		}
		batch := requests[i:end]
		_, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{
				s.table: batch,
			},
		})
		if err != nil {
			return fmt.Errorf("AutoConfig cache DynamoDB batch write: %w", err)
		}
	}
	return nil
}

func (s *dynamoDBStore) Close() error {
	return nil
}
