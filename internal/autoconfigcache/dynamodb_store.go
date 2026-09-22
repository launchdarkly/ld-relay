package autoconfigcache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
)

const (
	dynamoDBPartitionKey = "namespace"
	dynamoDBSortKey      = "key"
	dynamoDBItemAttr     = "item"
	dynamoDBMaxItemSize  = 400000
	dynamoDBMaxBatchSize = 25
)

type dynamoDBStore struct {
	client    *dynamodb.Client
	ctx       context.Context
	cancel    context.CancelFunc
	table     string
	namespace string
	encKey    []byte
	logger    *slog.Logger
}

func newDynamoDBStore(dbConfig config.DynamoDBConfig, cacheKey string, encKey []byte, logger *slog.Logger) (Store, error) {
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
	ctx, cancel := context.WithCancel(context.Background())
	return &dynamoDBStore{
		client:    client,
		ctx:       ctx,
		cancel:    cancel,
		table:     dbConfig.TableName,
		namespace: cacheKey,
		encKey:    encKey,
		logger:    logger,
	}, nil
}

func (s *dynamoDBStore) GetAll(ctx context.Context) (*autoconfig.PutContent, error) {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	content := &autoconfig.PutContent{
		Environments: make(map[config.EnvironmentID]envfactory.EnvironmentRep),
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
				s.logger.Warn("AutoConfig cache: failed to decrypt item", "key", sortKey, "error", err)
				continue
			}

			cached, err := unmarshalCachedItem(plaintext)
			if err != nil {
				s.logger.Warn("AutoConfig cache: skipping item", "key", sortKey, "error", err)
				continue
			}

			switch cached.Kind {
			case ModelKindEnvironment:
				envID := config.EnvironmentID(strings.TrimPrefix(sortKey, envItemPrefix))
				var rep envfactory.EnvironmentRep
				if err := json.Unmarshal(cached.Data, &rep); err != nil {
					s.logger.Warn("AutoConfig cache: failed to unmarshal env", "envId", envID, "error", err)
					continue
				}
				content.Environments[envID] = rep
			case ModelKindFilter:
				// Payload filters do not exist, so a row left by an earlier version is ignored
				// without comment. The case is here only to keep such rows out of the unknown-kind
				// branch below, which would log a warning on every read.
			default:
				s.logger.Warn("AutoConfig cache: skipping item with unknown kind", "key", sortKey, "kind", cached.Kind)
			}
		}

		if out.LastEvaluatedKey == nil {
			break
		}
		exclusiveStartKey = out.LastEvaluatedKey
	}

	if len(content.Environments) == 0 {
		return nil, nil
	}
	return content, nil
}

func (s *dynamoDBStore) SetAll(ctx context.Context, content autoconfig.PutContent) error {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
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

	// Step 2: Build write requests for new items. dropped counts anything that cannot be
	// written, so SetAll can tell the caller the snapshot is incomplete.
	var writeRequests []types.WriteRequest
	newKeys := make(map[string]bool)
	dropped := 0

	for id, rep := range content.Environments {
		if id != rep.EnvID {
			continue
		}
		sortKey := envItemPrefix + string(id)
		newKeys[sortKey] = true
		item, err := s.buildItem(sortKey, ModelKindEnvironment, rep)
		if err != nil {
			s.logger.Warn("AutoConfig cache: failed to build env item", "envId", id, "error", err)
			dropped++
			continue
		}
		if !s.checkSizeLimit(item, sortKey) {
			dropped++
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

	// Step 4: Batch write in chunks of 25. A failed chunk does not abort the rest, because a
	// partially written cache is worth more than none, but it is reported: a caller told the
	// write succeeded would later read an incomplete snapshot and treat it as the whole
	// configuration.
	total := len(writeRequests) + dropped
	dropped += s.batchWrite(ctx, writeRequests)
	if dropped > 0 {
		return fmt.Errorf("AutoConfig cache: %d of %d items could not be written, "+
			"so the cached configuration is incomplete", dropped, total)
	}
	return nil
}

func (s *dynamoDBStore) buildItem(sortKey string, kind ModelKind, value interface{}) (map[string]types.AttributeValue, error) {
	raw, err := marshalCachedItem(kind, value)
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
	s.logger.Error("AutoConfig cache item too large to store in DynamoDB and was dropped", "key", sortKey, "namespace", s.namespace)
	return false
}

// batchWrite writes in chunks of 25 and returns how many items it could not write. It keeps
// going after a failed chunk, because a partially written cache is still worth more than none,
// but the caller has to know the snapshot is incomplete rather than be told it succeeded.
func (s *dynamoDBStore) batchWrite(ctx context.Context, requests []types.WriteRequest) int {
	dropped := 0
	for i := 0; i < len(requests); i += dynamoDBMaxBatchSize {
		end := i + dynamoDBMaxBatchSize
		if end > len(requests) {
			end = len(requests)
		}
		out, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{s.table: requests[i:end]},
		})
		if err != nil {
			s.logger.Warn("AutoConfig cache DynamoDB batch write failed (continuing)", "error", err)
			dropped += end - i
			continue
		}
		unprocessed := 0
		for _, reqs := range out.UnprocessedItems {
			unprocessed += len(reqs)
		}
		if unprocessed > 0 {
			s.logger.Warn("AutoConfig cache DynamoDB: items were unprocessed and dropped", "count", unprocessed)
			dropped += unprocessed
		}
	}
	return dropped
}

func (s *dynamoDBStore) Upsert(ctx context.Context, kind autoconfig.CacheKind, id string, data interface{}) error {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	sortKey := cacheField(kind, id)
	item, err := s.buildItem(sortKey, modelKindFromCacheKind(kind), data)
	if err != nil {
		return fmt.Errorf("AutoConfig cache: failed to build item %q: %w", sortKey, err)
	}
	if !s.checkSizeLimit(item, sortKey) {
		return nil
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item:      item,
	})
	return err
}

func (s *dynamoDBStore) Delete(ctx context.Context, kind autoconfig.CacheKind, id string) error {
	ctx, cleanup := mergeContext(ctx, s.ctx)
	defer cleanup()
	sortKey := cacheField(kind, id)
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.table),
		Key: map[string]types.AttributeValue{
			dynamoDBPartitionKey: &types.AttributeValueMemberS{Value: s.namespace},
			dynamoDBSortKey:      &types.AttributeValueMemberS{Value: sortKey},
		},
	})
	return err
}

func (s *dynamoDBStore) Close() error {
	s.cancel()
	return nil
}
