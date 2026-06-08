package awsredisauth

import (
	"context"
	"fmt"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/launchdarkly/go-sdk-common/v4/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
)

// providerCacheKey is the fingerprint used to deduplicate SharedTokenProvider calls.
// Fields that affect the signed token or credential-loading behaviour are included;
// fields that are irrelevant to the provider itself (e.g. host/port/TLS) are omitted.
type providerCacheKey struct {
	cacheName  string
	username   string
	region     string
	serverless bool
}

// sharedProviderEntry is a cached provider plus its resolved region (for logging).
type sharedProviderEntry struct {
	provider       TokenProvider
	resolvedRegion string
}

var (
	sharedProvidersMu sync.Mutex
	sharedProviders   = map[providerCacheKey]sharedProviderEntry{} //nolint:gochecknoglobals
)

// ResetSharedTokenProvidersForTest clears the shared provider cache. It must only be
// called from tests; calling it in production code causes data races and re-probes.
func ResetSharedTokenProvidersForTest() {
	sharedProvidersMu.Lock()
	sharedProviders = map[providerCacheKey]sharedProviderEntry{}
	sharedProvidersMu.Unlock()
}

// SharedTokenProvider returns a memoized TokenProvider for the given RedisConfig.
// On the first call for a given (cacheName, username, region, serverless) fingerprint
// it loads the AWS default config, constructs a TokenProvider, performs a fail-fast
// Token() probe, and logs a startup info line. Subsequent calls with the same
// fingerprint return the cached instance without re-probing or re-logging.
//
// The function is concurrency-safe.
//
// Callers must only invoke this function when redisConfig.AWSAuth is true.
func SharedTokenProvider(ctx context.Context, redisConfig config.RedisConfig, loggers ldlog.Loggers) (TokenProvider, error) {
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

	// Not cached yet — build a new provider.
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsredisauth: loading AWS config: %w", err)
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

	// Fail-fast probe: call Token() once at startup. This surfaces misconfigured
	// credentials or a missing/unresolvable AWS region before any Redis connection
	// is attempted.
	if _, err := provider.Token(ctx); err != nil {
		return nil, fmt.Errorf("awsredisauth: startup token verification failed: %w", err)
	}

	// Resolve the effective region for the log line. Options.Region takes precedence;
	// fall back to whatever the SDK resolved from the credential chain.
	resolvedRegion := provider.(*tokenProvider).region //nolint:forcetypeassert

	loggers.Infof("ElastiCache IAM auth enabled (cache=%s, region=%s, serverless=%t)",
		redisConfig.AWSCacheName, resolvedRegion, redisConfig.AWSServerless)

	entry := sharedProviderEntry{
		provider:       provider,
		resolvedRegion: resolvedRegion,
	}
	sharedProviders[key] = entry
	return provider, nil
}
