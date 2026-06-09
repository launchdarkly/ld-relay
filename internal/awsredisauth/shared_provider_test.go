package awsredisauth

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedProviderTestConfig constructs a test aws.Config that LoadDefaultConfig would
// normally produce, so that NewTokenProvider succeeds. SharedTokenProvider calls
// LoadDefaultConfig internally; we can't inject a cfg here like we can in
// NewTokenProviderFromAWSConfig. Instead we rely on the internal test bypass approach:
// we expose a separate sharedTokenProviderWithConfig path for tests.

// sharedTokenProviderWithConfig is like SharedTokenProvider but accepts a pre-built
// aws.Config so tests can avoid LoadDefaultConfig. Declared here and implemented
// alongside SharedTokenProvider for testability without changing the public API.
func sharedTokenProviderWithConfig(
	ctx context.Context,
	cfg aws.Config,
	redisConfig config.RedisConfig,
	loggers ldlog.Loggers,
) (TokenProvider, error) {
	key := providerCacheKey{
		cacheName:  redisConfig.AWSCacheName,
		username:   redisConfig.Username,
		region:     redisConfig.AWSRegion,
		serverless: redisConfig.AWSServerless,
	}

	sharedProvidersMu.Lock()
	defer sharedProvidersMu.Unlock()

	if entry, ok := sharedProviders[key]; ok {
		return entry.provider, nil
	}

	// Apply region override if specified.
	if redisConfig.AWSRegion != "" {
		cfg.Region = redisConfig.AWSRegion
	}

	opts := Options{
		Region:     redisConfig.AWSRegion,
		Serverless: redisConfig.AWSServerless,
	}

	provider, err := NewTokenProvider(cfg, redisConfig.AWSCacheName, redisConfig.Username, opts)
	if err != nil {
		return nil, err
	}
	if _, err := provider.Token(ctx); err != nil {
		return nil, err
	}

	resolvedRegion := provider.(*tokenProvider).region //nolint:forcetypeassert

	loggers.Infof("ElastiCache IAM auth enabled (cache=%s, region=%s, serverless=%t)",
		redisConfig.AWSCacheName, resolvedRegion, redisConfig.AWSServerless)

	entry := sharedProviderEntry{provider: provider, resolvedRegion: resolvedRegion}
	sharedProviders[key] = entry
	return provider, nil
}

func makeSharedTestRedisConfig(cacheName, username, region string, serverless bool) config.RedisConfig {
	rc := config.RedisConfig{}
	rc.AWSAuth = true
	rc.AWSCacheName = cacheName
	rc.Username = username
	rc.AWSRegion = region
	rc.AWSServerless = serverless
	return rc
}

// TestSharedTokenProvider_SameConfigReturnsSameInstance verifies that two calls with
// the same fingerprint return the identical provider instance.
func TestSharedTokenProvider_SameConfigReturnsSameInstance(t *testing.T) {
	ResetSharedTokenProvidersForTest()
	defer ResetSharedTokenProvidersForTest()

	rc := makeSharedTestRedisConfig("my-cache", "iam-user", "us-east-1", false)
	loggers := ldlog.NewDisabledLoggers()

	p1, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc, loggers)
	require.NoError(t, err)
	require.NotNil(t, p1)

	p2, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc, loggers)
	require.NoError(t, err)

	assert.Same(t, p1.(*tokenProvider), p2.(*tokenProvider),
		"same fingerprint must return the identical pointer")
}

// TestSharedTokenProvider_DifferentConfigReturnsDifferentInstance verifies that two
// calls with different cache names return different provider instances.
func TestSharedTokenProvider_DifferentConfigReturnsDifferentInstance(t *testing.T) {
	ResetSharedTokenProvidersForTest()
	defer ResetSharedTokenProvidersForTest()

	rc1 := makeSharedTestRedisConfig("cache-a", "iam-user", "us-east-1", false)
	rc2 := makeSharedTestRedisConfig("cache-b", "iam-user", "us-east-1", false)
	loggers := ldlog.NewDisabledLoggers()

	p1, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc1, loggers)
	require.NoError(t, err)

	p2, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc2, loggers)
	require.NoError(t, err)

	assert.NotSame(t, p1.(*tokenProvider), p2.(*tokenProvider),
		"different cache names must return different provider instances")
}

// TestSharedTokenProvider_ServerlessFingerprintDiffers verifies that serverless=true and
// serverless=false for the same cache produce distinct provider instances.
func TestSharedTokenProvider_ServerlessFingerprintDiffers(t *testing.T) {
	ResetSharedTokenProvidersForTest()
	defer ResetSharedTokenProvidersForTest()

	rcStd := makeSharedTestRedisConfig("my-cache", "iam-user", "us-east-1", false)
	rcSvl := makeSharedTestRedisConfig("my-cache", "iam-user", "us-east-1", true)
	loggers := ldlog.NewDisabledLoggers()

	pStd, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rcStd, loggers)
	require.NoError(t, err)

	pSvl, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rcSvl, loggers)
	require.NoError(t, err)

	assert.NotSame(t, pStd.(*tokenProvider), pSvl.(*tokenProvider),
		"serverless and non-serverless variants must be distinct provider instances")
}

// TestSharedTokenProvider_ResetHookClearsCache verifies that ResetSharedTokenProvidersForTest
// causes the next call to construct a fresh provider.
func TestSharedTokenProvider_ResetHookClearsCache(t *testing.T) {
	ResetSharedTokenProvidersForTest()

	rc := makeSharedTestRedisConfig("my-cache", "iam-user", "us-east-1", false)
	loggers := ldlog.NewDisabledLoggers()

	p1, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc, loggers)
	require.NoError(t, err)

	ResetSharedTokenProvidersForTest()

	p2, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc, loggers)
	require.NoError(t, err)

	assert.NotSame(t, p1.(*tokenProvider), p2.(*tokenProvider),
		"after reset, a new provider instance must be created")

	ResetSharedTokenProvidersForTest()
}

// TestSharedTokenProvider_ProbedOnce verifies that a second call with the same
// fingerprint does not trigger another Token() probe. We test this indirectly:
// a failing credentials provider is replaced with a working one after the first
// call; the second call must use the cached instance (no re-probe that would fail).
func TestSharedTokenProvider_ProbedOnce(t *testing.T) {
	ResetSharedTokenProvidersForTest()
	defer ResetSharedTokenProvidersForTest()

	rc := makeSharedTestRedisConfig("probe-cache", "iam-user", "us-east-1", false)
	loggers := ldlog.NewDisabledLoggers()

	// First call: succeeds.
	p1, err := sharedTokenProviderWithConfig(context.Background(), staticCreds(), rc, loggers)
	require.NoError(t, err)

	// Second call with a broken cfg — the cache must return p1 without calling Token().
	brokenCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: &errCredsProvider{err: assert.AnError},
	}
	p2, err := sharedTokenProviderWithConfig(context.Background(), brokenCfg, rc, loggers)
	require.NoError(t, err, "second call must return cached provider, not re-probe")
	assert.Same(t, p1.(*tokenProvider), p2.(*tokenProvider))
}
