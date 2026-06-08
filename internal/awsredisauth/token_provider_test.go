package awsredisauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRegion    = "us-east-1"
	testCacheName = "my-cache"
	testUser      = "iam-user-01"
)

// staticCreds returns an aws.Config with a static credentials provider
// suitable for deterministic signing in tests.
func staticCreds() aws.Config {
	return aws.Config{
		Region: testRegion,
		Credentials: credentials.NewStaticCredentialsProvider(
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"",
		),
	}
}

// mustNewProvider is a test helper that calls NewTokenProvider and requires no error.
func mustNewProvider(t *testing.T, cfg aws.Config, cacheName, user string, opts ...Options) TokenProvider {
	t.Helper()
	p, err := NewTokenProvider(cfg, cacheName, user, opts...)
	require.NoError(t, err)
	return p
}

// parseToken parses the token returned by Token() back into a *url.URL.
// The token is a scheme-stripped URL, so we re-add "https://" to parse it.
func parseToken(t *testing.T, token string) *url.URL {
	t.Helper()
	u, err := url.Parse("https://" + token)
	require.NoError(t, err, "token must be a valid URL path with query string")
	return u
}

// TestToken_QueryParams verifies that the generated token contains the expected
// query parameters: Action=connect, User=<user>, X-Amz-Expires=900 (default),
// and a non-empty X-Amz-Signature. The scheme must be https and the host must
// be the cache name.
func TestToken_QueryParams(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), testCacheName, testUser)

	token, err := p.Token(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, token, "token must not be empty")

	// Token is a scheme-stripped URL; restore scheme to parse.
	u := parseToken(t, token)

	assert.Equal(t, "https", u.Scheme)
	assert.Equal(t, testCacheName, u.Hostname(), "host must be the cache name")

	q := u.Query()
	assert.Equal(t, "connect", q.Get("Action"), "Action must be 'connect'")
	assert.Equal(t, testUser, q.Get("User"), "User must match")
	assert.Equal(t, "900", q.Get("X-Amz-Expires"), "default X-Amz-Expires must be 900")
	assert.NotEmpty(t, q.Get("X-Amz-Signature"), "X-Amz-Signature must be present")
}

// TestToken_CustomTokenLifetime verifies that Options.TokenLifetime overrides the
// X-Amz-Expires query parameter in the generated token.
func TestToken_CustomTokenLifetime(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), testCacheName, testUser, Options{
		TokenLifetime: 5 * time.Second,
	})

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	assert.Equal(t, "5", u.Query().Get("X-Amz-Expires"), "X-Amz-Expires must reflect custom lifetime")
}

// TestToken_RegionInCredential verifies that the signing region flows through into
// the X-Amz-Credential scope of the generated token.
//
// X-Amz-Credential takes the form: <AKID>/<date>/<region>/elasticache/aws4_request
func TestToken_RegionInCredential(t *testing.T) {
	const customRegion = "eu-west-2"

	cfg := aws.Config{
		Region: customRegion,
		Credentials: credentials.NewStaticCredentialsProvider(
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"",
		),
	}

	p := mustNewProvider(t, cfg, testCacheName, testUser)

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	credential := u.Query().Get("X-Amz-Credential")
	require.NotEmpty(t, credential, "X-Amz-Credential must be present")

	// X-Amz-Credential = AKID/YYYYMMDD/<region>/elasticache/aws4_request
	assert.True(t,
		strings.Contains(credential, "/"+customRegion+"/elasticache/aws4_request"),
		"X-Amz-Credential %q must contain /<region>/elasticache/aws4_request; got: %s",
		credential, credential,
	)
}

// TestToken_OptionsRegionOverridesCfgRegion verifies that Options.Region takes
// precedence over cfg.Region when both are provided.
func TestToken_OptionsRegionOverridesCfgRegion(t *testing.T) {
	const optRegion = "ap-southeast-1"

	cfg := aws.Config{
		Region: "us-east-1", // should be ignored
		Credentials: credentials.NewStaticCredentialsProvider(
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"",
		),
	}

	p := mustNewProvider(t, cfg, testCacheName, testUser, Options{Region: optRegion})

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	credential := u.Query().Get("X-Amz-Credential")
	assert.True(t,
		strings.Contains(credential, "/"+optRegion+"/elasticache/aws4_request"),
		"credential must use Options.Region %q; got %s", optRegion, credential,
	)
}

// TestToken_CredentialsRetrieveErrorPropagates verifies that when the credentials
// provider returns an error, Token() surfaces that error.
func TestToken_CredentialsRetrieveErrorPropagates(t *testing.T) {
	sentinelErr := errors.New("no credentials available")

	cfg := aws.Config{
		Region:      testRegion,
		Credentials: &errCredsProvider{err: sentinelErr},
	}

	p := mustNewProvider(t, cfg, testCacheName, testUser)

	_, err := p.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinelErr, "credentials error must be wrapped and propagated")
}

// TestNewTokenProvider_EmptyRegionErrors verifies that NewTokenProvider returns
// an error at construction time when neither cfg.Region nor Options.Region is set.
func TestNewTokenProvider_EmptyRegionErrors(t *testing.T) {
	cfg := aws.Config{
		// Region intentionally empty
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}

	_, err := NewTokenProvider(cfg, testCacheName, testUser)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region is required")
}

// TestNewTokenProvider_NilCredentialsErrors verifies that NewTokenProvider returns
// an error at construction time when cfg.Credentials is nil.
func TestNewTokenProvider_NilCredentialsErrors(t *testing.T) {
	cfg := aws.Config{
		Region:      testRegion,
		Credentials: nil,
	}

	_, err := NewTokenProvider(cfg, testCacheName, testUser)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Credentials must not be nil")
}

// TestToken_CacheNameLowercased verifies that the cache name is lowercased
// defensively, so the URL host matches what AWS expects.
func TestToken_CacheNameLowercased(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), "My-Cache", testUser)

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	assert.Equal(t, "my-cache", u.Hostname(), "cache name must be lowercased")
}

// TestToken_PerCallFreshness verifies that calling Token() twice produces two
// non-identical tokens (different X-Amz-Date when called more than 1 second apart,
// or at least that both calls succeed independently).
//
// Note: because the AWS SDK v4 signer does not expose a clock injection seam,
// we cannot deterministically control the signing time. We therefore verify only
// that two sequential calls both succeed and produce non-empty tokens. The
// stateless design guarantees freshness architecturally (no cache means each
// call re-signs).
func TestToken_PerCallFreshness(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), testCacheName, testUser)

	tok1, err1 := p.Token(context.Background())
	require.NoError(t, err1)
	require.NotEmpty(t, tok1)

	tok2, err2 := p.Token(context.Background())
	require.NoError(t, err2)
	require.NotEmpty(t, tok2)

	// Both tokens must be valid signed URLs.
	u1 := parseToken(t, tok1)
	u2 := parseToken(t, tok2)
	assert.NotEmpty(t, u1.Query().Get("X-Amz-Signature"))
	assert.NotEmpty(t, u2.Query().Get("X-Amz-Signature"))

	// Note: tok1 == tok2 is possible if both calls happen within the same second
	// (same X-Amz-Date). That is acceptable and does not indicate a bug.
}

// TestToken_Serverless_ResourceTypePresent verifies that when Options.Serverless=true,
// the token URL contains ResourceType=ServerlessCache.
func TestToken_Serverless_ResourceTypePresent(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), testCacheName, testUser, Options{Serverless: true})

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	assert.Equal(t, "ServerlessCache", u.Query().Get("ResourceType"),
		"Serverless token must contain ResourceType=ServerlessCache")
}

// TestToken_Serverless_ResourceTypeAbsent verifies that when Options.Serverless=false
// (the default), the token URL does NOT contain a ResourceType parameter.
func TestToken_Serverless_ResourceTypeAbsent(t *testing.T) {
	p := mustNewProvider(t, staticCreds(), testCacheName, testUser)

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	assert.Empty(t, u.Query().Get("ResourceType"),
		"non-Serverless token must not contain ResourceType")
}

// TestToken_OptionsRegionFlowsThroughToCredential verifies that Options.Region appears
// in the X-Amz-Credential scope of the generated token, overriding cfg.Region.
func TestToken_OptionsRegionFlowsThroughToCredential(t *testing.T) {
	const overrideRegion = "eu-central-1"

	cfg := aws.Config{
		Region: "us-east-1", // should be overridden
		Credentials: credentials.NewStaticCredentialsProvider(
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"",
		),
	}

	p := mustNewProvider(t, cfg, testCacheName, testUser, Options{Region: overrideRegion})

	token, err := p.Token(context.Background())
	require.NoError(t, err)

	u := parseToken(t, token)
	credential := u.Query().Get("X-Amz-Credential")
	require.NotEmpty(t, credential)
	assert.True(t,
		strings.Contains(credential, "/"+overrideRegion+"/elasticache/aws4_request"),
		"X-Amz-Credential %q must use Options.Region %q", credential, overrideRegion,
	)
}

// errCredsProvider is a test-only aws.CredentialsProvider that always returns an error.
type errCredsProvider struct {
	err error
}

func (e *errCredsProvider) Retrieve(_ context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, e.err
}
