package awsredisauth

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/launchdarkly/ld-relay/v8/config"
)

// NewTokenProviderFromRedisConfig constructs a TokenProvider for the given RedisConfig.
// It loads AWS credentials via the SDK default chain (environment variables, ~/.aws/credentials,
// IRSA web-identity token, EKS Pod Identity, etc.), then performs a fail-fast verification
// by calling Token() once. Any error — missing credentials, empty region, or network failure
// during the initial STS call — is returned immediately so relay startup fails with a clear
// message rather than silently deferring the failure to the first Redis connection.
//
// This is the recommended entry point for all relay wiring sites. Callers must only invoke
// this function when redisConfig.AWSAuth is true.
func NewTokenProviderFromRedisConfig(ctx context.Context, redisConfig config.RedisConfig) (TokenProvider, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsredisauth: loading AWS config: %w", err)
	}
	return NewTokenProviderFromAWSConfig(ctx, cfg, redisConfig)
}

// NewTokenProviderFromAWSConfig is like NewTokenProviderFromRedisConfig but accepts a
// pre-constructed aws.Config rather than loading one from the default chain. This is the
// testable entry point used by unit tests that need to inject a controlled aws.Config
// (e.g., a credentials provider that returns an error).
func NewTokenProviderFromAWSConfig(ctx context.Context, cfg aws.Config, redisConfig config.RedisConfig) (TokenProvider, error) {
	opts := Options{
		Region:     redisConfig.AWSRegion,
		Serverless: redisConfig.AWSServerless,
	}
	provider, err := NewTokenProvider(cfg, redisConfig.AWSCacheName, redisConfig.Username, opts)
	if err != nil {
		return nil, err
	}

	// Fail-fast verification: call Token() once at startup. This surfaces misconfigured
	// credentials or a missing AWS region before any Redis connection is attempted.
	if _, err := provider.Token(ctx); err != nil {
		return nil, fmt.Errorf("awsredisauth: startup token verification failed: %w", err)
	}

	return provider, nil
}
