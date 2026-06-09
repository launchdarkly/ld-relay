package sdks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
	ldredis "github.com/launchdarkly/go-server-sdk-redis-redigo/v3"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/awsredisauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errCredsProvider is a test-only aws.CredentialsProvider that always returns an error.
type errCredsProvider struct {
	err error
}

func (e *errCredsProvider) Retrieve(_ context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, e.err
}

// staticAWSConfig returns an aws.Config with deterministic static credentials for tests.
func staticAWSConfig() aws.Config {
	return aws.Config{
		Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"",
		),
	}
}

// awsRedisConfig returns a RedisConfig with AWSAuth=true and required companion fields.
func awsRedisConfig() config.RedisConfig {
	rc := config.RedisConfig{}
	rc.URL, _ = configtypes.NewOptURLAbsoluteFromString("rediss://my-cache.abc123.use1.cache.amazonaws.com:6379")
	rc.AWSAuth = true
	rc.AWSCacheName = "my-cache"
	rc.Username = "iam-user-01"
	rc.TLS = true
	return rc
}

// makeRedisDataStoreBuilderWithProvider is a test helper that bypasses LoadDefaultConfig
// by injecting a pre-constructed TokenProvider directly into the builder. This lets unit
// tests verify the builder wiring without needing real AWS credentials.
func makeRedisDataStoreBuilderWithProvider[T any](
	constructor func() *ldredis.StoreBuilder[T],
	allConfig config.Config,
	envConfig config.EnvConfig,
	provider awsredisauth.TokenProvider,
) (*ldredis.StoreBuilder[T], string) {
	redisURL, prefix := GetRedisBasicProperties(allConfig.Redis, envConfig)
	b := constructor().
		URL(redisURL).
		Prefix(prefix).
		PasswordProvider(provider.Token).
		MaxConnLifetime(11 * time.Hour)
	return b, redisURL
}

// TestMakeRedisDataStoreBuilder_AWSAuthCredentialsError verifies that when AWSAuth=true
// and the AWS credentials provider returns an error, the fail-fast token verification in
// NewTokenProviderFromAWSConfig surfaces that error at construction time.
func TestMakeRedisDataStoreBuilder_AWSAuthCredentialsError(t *testing.T) {
	sentinelErr := errors.New("no credentials available")

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: &errCredsProvider{err: sentinelErr},
	}
	rc := awsRedisConfig()

	_, err := awsredisauth.NewTokenProviderFromAWSConfig(context.Background(), cfg, rc)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinelErr)
}

// TestMakeRedisDataStoreBuilder_AWSAuthSuccess verifies that when AWSAuth=true and a valid
// aws.Config is available, a non-nil builder is returned with AWS provider wired in.
func TestMakeRedisDataStoreBuilder_AWSAuthSuccess(t *testing.T) {
	provider, err := awsredisauth.NewTokenProviderFromAWSConfig(context.Background(), staticAWSConfig(), awsRedisConfig())
	require.NoError(t, err)
	require.NotNil(t, provider)

	allConfig := config.Config{Redis: awsRedisConfig()}
	b, _ := makeRedisDataStoreBuilderWithProvider(ldredis.DataStore, allConfig, config.EnvConfig{}, provider)
	assert.NotNil(t, b)
}

// TestConfigureDataStore_AWSAuth_Error verifies that ConfigureDataStore with AWSAuth=true
// returns an error when no real AWS credentials are available (CI environment). This confirms
// the error from makeRedisDataStoreBuilder propagates up through ConfigureDataStore.
func TestConfigureDataStore_AWSAuth_Error(t *testing.T) {
	awsredisauth.ResetSharedTokenProvidersForTest()
	defer awsredisauth.ResetSharedTokenProvidersForTest()

	allConfig := config.Config{Redis: awsRedisConfig()}
	_, _, err := ConfigureDataStore(allConfig, config.EnvConfig{}, ldlog.NewDisabledLoggers())
	// In CI (no AWS credentials or region configured), startup must fail fast.
	assert.Error(t, err)
}
