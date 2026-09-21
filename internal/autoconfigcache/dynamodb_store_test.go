package autoconfigcache

// Unit tests for the DynamoDB AutoConfig cache store. They run the real AWS SDK client against an
// httptest server that speaks the DynamoDB wire protocol, so no local DynamoDB is needed and the
// tests run in normal CI. The build-tagged tests in internal/bigsegments cover a real database;
// these cover the store's own accounting, which a real database cannot be made to produce on demand.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDynamoDBTable     = "test-table"
	testDynamoDBNamespace = "test-namespace"
)

// fakeDynamoDB answers the two operations SetAll uses. Query reports an empty table, so no stale
// items are deleted. batchWriteResponse decides what each BatchWriteItem call returns.
type fakeDynamoDB struct {
	batchWriteResponse func(callNum int, requestedItems int) (status int, body string)

	mu         sync.Mutex
	batchCalls []int // items requested in each BatchWriteItem call, in order
}

func (f *fakeDynamoDB) start(t *testing.T) string {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		switch {
		case strings.HasSuffix(target, ".Query"):
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			_, _ = w.Write([]byte(`{"Count":0,"ScannedCount":0,"Items":[]}`))
		case strings.HasSuffix(target, ".BatchWriteItem"):
			var input struct {
				RequestItems map[string][]json.RawMessage `json:"RequestItems"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			requested := len(input.RequestItems[testDynamoDBTable])

			f.mu.Lock()
			f.batchCalls = append(f.batchCalls, requested)
			callNum := len(f.batchCalls)
			f.mu.Unlock()

			status, body := http.StatusOK, `{}`
			if f.batchWriteResponse != nil {
				status, body = f.batchWriteResponse(callNum, requested)
			}
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected DynamoDB operation %q", target)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (f *fakeDynamoDB) calls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.batchCalls...)
}

// unprocessedItems builds a BatchWriteItem response body that reports n items as unprocessed. The
// store only counts them, so a minimal write request per item is enough.
func unprocessedItems(n int) string {
	requests := make([]string, 0, n)
	for i := 0; i < n; i++ {
		requests = append(requests, fmt.Sprintf(`{"PutRequest":{"Item":{"key":{"S":"unprocessed-%d"}}}}`, i))
	}
	return fmt.Sprintf(`{"UnprocessedItems":{%q:[%s]}}`, testDynamoDBTable, strings.Join(requests, ","))
}

func newTestDynamoDBStore(t *testing.T, endpoint string) *dynamoDBStore {
	client := dynamodb.New(dynamodb.Options{
		Region:           "us-east-1",
		Credentials:      credentials.NewStaticCredentialsProvider("key", "secret", ""),
		BaseEndpoint:     aws.String(endpoint),
		RetryMaxAttempts: 1, // keep the call count deterministic
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &dynamoDBStore{
		client:    client,
		ctx:       ctx,
		cancel:    cancel,
		table:     testDynamoDBTable,
		namespace: testDynamoDBNamespace,
		encKey:    deriveKey([]byte("test-encryption-key")),
		logger:    slog.New(slog.DiscardHandler),
	}
}

// testContent builds a PutContent with n environments, each small enough to store.
func testContent(n int) autoconfig.PutContent {
	envs := make(map[config.EnvironmentID]envfactory.EnvironmentRep, n)
	for i := 0; i < n; i++ {
		id := config.EnvironmentID(fmt.Sprintf("env-%d", i))
		envs[id] = envfactory.EnvironmentRep{EnvID: id, EnvKey: "key", EnvName: "name", Version: 1}
	}
	return autoconfig.PutContent{Environments: envs}
}

func TestSetAllSucceedsWhenEveryItemIsWritten(t *testing.T) {
	fake := &fakeDynamoDB{}
	store := newTestDynamoDBStore(t, fake.start(t))

	require.NoError(t, store.SetAll(context.Background(), testContent(3)))
	assert.Equal(t, []int{3}, fake.calls())
}

func TestSetAllCountsUnprocessedItemsAgainstTheItemTotal(t *testing.T) {
	fake := &fakeDynamoDB{
		batchWriteResponse: func(int, int) (int, string) {
			return http.StatusOK, unprocessedItems(1)
		},
	}
	store := newTestDynamoDBStore(t, fake.start(t))

	err := store.SetAll(context.Background(), testContent(3))

	// The total is the 3 items SetAll set out to write. An unprocessed item must not be counted
	// once as a write request and again as a drop, which would report "1 of 4".
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 3 items could not be written")
}

func TestSetAllCountsAFailedBatchAgainstTheItemTotal(t *testing.T) {
	fake := &fakeDynamoDB{
		batchWriteResponse: func(int, int) (int, string) {
			return http.StatusInternalServerError,
				`{"__type":"com.amazonaws.dynamodb.v20120810#InternalServerError","message":"boom"}`
		},
	}
	store := newTestDynamoDBStore(t, fake.start(t))

	err := store.SetAll(context.Background(), testContent(3))

	// Every item in the failed chunk is dropped, and every item was one SetAll meant to write,
	// so the count and the total are the same. Double counting would report "3 of 6".
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3 of 3 items could not be written")
}

func TestSetAllCountsAnOversizedItemAgainstTheItemTotal(t *testing.T) {
	fake := &fakeDynamoDB{}
	store := newTestDynamoDBStore(t, fake.start(t))

	content := testContent(1)
	oversizedID := config.EnvironmentID("env-oversized")
	content.Environments[oversizedID] = envfactory.EnvironmentRep{
		EnvID:   oversizedID,
		EnvName: strings.Repeat("x", dynamoDBMaxItemSize+1),
		Version: 1,
	}

	err := store.SetAll(context.Background(), content)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 2 items could not be written")
	// The item that fits is still written, because a partial cache beats none.
	assert.Equal(t, []int{1}, fake.calls())
}

func TestSetAllWritesInChunksOfTwentyFive(t *testing.T) {
	fake := &fakeDynamoDB{}
	store := newTestDynamoDBStore(t, fake.start(t))

	require.NoError(t, store.SetAll(context.Background(), testContent(30)))
	assert.Equal(t, []int{25, 5}, fake.calls())
}
