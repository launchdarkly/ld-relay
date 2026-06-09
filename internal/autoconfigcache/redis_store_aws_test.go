package autoconfigcache

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
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

// awsRedisConfig returns a RedisConfig with AWSAuth=true and all required companion fields.
func awsRedisConfig() config.RedisConfig {
	rc := config.RedisConfig{}
	rc.URL, _ = configtypes.NewOptURLAbsoluteFromString("rediss://my-cache.abc123.use1.cache.amazonaws.com:6379")
	rc.AWSAuth = true
	rc.AWSCacheName = "my-cache"
	rc.Username = "iam-user-01"
	rc.TLS = true
	return rc
}

// TestNewRedisStore_AWSAuthCredentialsError verifies that when AWSAuth=true
// and the AWS credentials provider returns an error, NewTokenProviderFromAWSConfig
// returns that error at construction time (fail-fast token verification).
func TestNewRedisStore_AWSAuthCredentialsError(t *testing.T) {
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

// TestNewRedisStore_AWSAuthSuccess verifies that a valid aws.Config produces a usable provider.
func TestNewRedisStore_AWSAuthSuccess(t *testing.T) {
	provider, err := awsredisauth.NewTokenProviderFromAWSConfig(context.Background(), staticAWSConfig(), awsRedisConfig())
	require.NoError(t, err)
	require.NotNil(t, provider)
}

// TestNewRedisStore_AWSAuth_ErrorPropagated verifies that newRedisStore returns an error
// when AWSAuth=true and no real AWS credentials are available (CI environment).
func TestNewRedisStore_AWSAuth_ErrorPropagated(t *testing.T) {
	awsredisauth.ResetSharedTokenProvidersForTest()
	defer awsredisauth.ResetSharedTokenProvidersForTest()

	rc := awsRedisConfig()
	_, err := newRedisStore(rc, "test-cache-key", make([]byte, 32), ldlog.NewDisabledLoggers())
	// In CI (no AWS credentials or region), startup must fail fast.
	assert.Error(t, err)
}
