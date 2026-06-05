package awsredisauth

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTestRedisConfig returns a RedisConfig with AWSAuth=true and the required companion
// fields populated. The URL uses a dummy host; no actual connection is made in these tests.
func makeTestRedisConfig() config.RedisConfig {
	rc := config.RedisConfig{}
	rc.AWSAuth = true
	rc.AWSCacheName = "my-cache"
	rc.Username = "iam-user-01"
	return rc
}

// TestNewTokenProviderFromAWSConfig_CredentialsErrorPropagates verifies that when the
// aws.Config has a credentials provider that fails Retrieve, NewTokenProviderFromAWSConfig
// returns an error at construction time (fail-fast verification).
func TestNewTokenProviderFromAWSConfig_CredentialsErrorPropagates(t *testing.T) {
	sentinelErr := errors.New("no credentials available")

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: &errCredsProvider{err: sentinelErr},
	}

	_, err := NewTokenProviderFromAWSConfig(context.Background(), cfg, makeTestRedisConfig())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinelErr, "credentials error must be wrapped and propagated")
}

// TestNewTokenProviderFromAWSConfig_SuccessWithStaticCreds verifies that a valid aws.Config
// produces a usable TokenProvider.
func TestNewTokenProviderFromAWSConfig_SuccessWithStaticCreds(t *testing.T) {
	provider, err := NewTokenProviderFromAWSConfig(context.Background(), staticCreds(), makeTestRedisConfig())
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Provider should be usable immediately.
	tok, err := provider.Token(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
}
