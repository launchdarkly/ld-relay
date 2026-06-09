package awsredisauth

import (
	"context"
	"errors"
	"strings"
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

// TestNewTokenProviderFromAWSConfig_RegionOverride verifies that AWSRegion in
// RedisConfig is threaded through to the SigV4 signing region.
func TestNewTokenProviderFromAWSConfig_RegionOverride(t *testing.T) {
	const overrideRegion = "ap-northeast-1"

	rc := makeTestRedisConfig()
	rc.AWSRegion = overrideRegion

	// Provide a cfg with a different region to confirm override wins.
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: staticCreds().Credentials,
	}

	provider, err := NewTokenProviderFromAWSConfig(context.Background(), cfg, rc)
	require.NoError(t, err)

	tok, err := provider.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, tok)
	credential := u.Query().Get("X-Amz-Credential")
	assert.True(t,
		strings.Contains(credential, "/"+overrideRegion+"/elasticache/aws4_request"),
		"token credential %q must use AWSRegion override %q", credential, overrideRegion,
	)
}

// TestNewTokenProviderFromAWSConfig_ServerlessFlag verifies that AWSServerless=true in
// RedisConfig causes ResourceType=ServerlessCache in the generated token.
func TestNewTokenProviderFromAWSConfig_ServerlessFlag(t *testing.T) {
	rc := makeTestRedisConfig()
	rc.AWSServerless = true

	provider, err := NewTokenProviderFromAWSConfig(context.Background(), staticCreds(), rc)
	require.NoError(t, err)

	tok, err := provider.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, tok)
	assert.Equal(t, "ServerlessCache", u.Query().Get("ResourceType"),
		"AWSServerless=true must produce ResourceType=ServerlessCache in token")
}

// TestNewTokenProviderFromAWSConfig_ServerlessAbsent verifies that when AWSServerless
// is false (default), no ResourceType parameter appears in the token.
func TestNewTokenProviderFromAWSConfig_ServerlessAbsent(t *testing.T) {
	provider, err := NewTokenProviderFromAWSConfig(context.Background(), staticCreds(), makeTestRedisConfig())
	require.NoError(t, err)

	tok, err := provider.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, tok)
	assert.Empty(t, u.Query().Get("ResourceType"),
		"AWSServerless=false must not include ResourceType in token")
}
