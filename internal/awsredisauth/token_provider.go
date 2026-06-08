// Package awsredisauth generates SigV4-presigned authentication tokens for
// AWS ElastiCache IAM authentication. Each Token() call produces a fresh
// presigned URL (with the scheme stripped) that can be passed as the Redis
// AUTH password for a new connection.
//
// Token generation is stateless: no caching, no background refresh goroutines.
// The AWS SDK's credential cache (aws.CredentialsCache) handles Layer 1 (IAM
// credential refresh); this package owns only Layer 2 (per-connection token
// generation via SigV4 presigning).
package awsredisauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// elasticacheSigningService is the SigV4 service identifier for ElastiCache IAM auth.
	elasticacheSigningService = "elasticache"

	// emptyPayloadHash is the SHA-256 hash of an empty body, required by PresignHTTP.
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	// defaultTokenLifetime is the AWS-documented token validity window.
	defaultTokenLifetime = 15 * time.Minute
)

// TokenProvider generates ElastiCache IAM authentication tokens.
type TokenProvider interface {
	// Token generates a fresh SigV4-presigned authentication token.
	// The returned string is the presigned URL with the scheme stripped
	// (e.g. "my-cache/?Action=connect&User=iam-user&X-Amz-...").
	// Callers pass this as the password to Redis AUTH or HELLO.
	Token(ctx context.Context) (string, error)
}

// Options holds optional configuration for NewTokenProvider.
type Options struct {
	// Region overrides cfg.Region for SigV4 signing. If empty, cfg.Region is used.
	Region string

	// TokenLifetime overrides the default 15-minute token expiry (X-Amz-Expires).
	// Primarily useful in tests to shorten the validity window. If zero, defaults
	// to 15 minutes (900 seconds).
	TokenLifetime time.Duration

	// Serverless indicates that the target cache is an ElastiCache Serverless cache.
	// When true, the signed token URL includes the query parameter
	// ResourceType=ServerlessCache, which ElastiCache Serverless requires for IAM
	// authentication. Without it, the auth request fails with WRONGPASS.
	Serverless bool
}

// tokenProvider is the concrete, stateless implementation of TokenProvider.
type tokenProvider struct {
	creds     aws.CredentialsProvider
	signer    *v4.Signer
	endpoint  string // "https://<cacheName>/"
	baseQuery url.Values
	region    string
	expires   time.Duration
}

// NewTokenProvider constructs a stateless TokenProvider. On each Token() call
// it retrieves credentials from cfg.Credentials and presigns a SigV4 URL of
// the form:
//
//	https://<cacheName>/?Action=connect&User=<user>
//
// The token returned by Token() is the signed URL with the "https://" scheme
// stripped, as required by ElastiCache IAM auth.
//
// Returns an error immediately if the resolved region is empty — that is a
// startup misconfiguration and is not recoverable without operator intervention.
//
// cacheName is lowercased defensively; callers should already supply lowercase
// per AWS requirements (cache names are converted to lowercase at creation time).
func NewTokenProvider(cfg aws.Config, cacheName, user string, opts ...Options) (TokenProvider, error) {
	if cfg.Credentials == nil {
		return nil, errors.New("awsredisauth: cfg.Credentials must not be nil")
	}

	opt := Options{}
	if len(opts) > 0 {
		opt = opts[0]
	}

	region := opt.Region
	if region == "" {
		region = cfg.Region
	}
	if region == "" {
		return nil, errors.New("awsredisauth: region is required; set cfg.Region or Options.Region")
	}

	lifetime := opt.TokenLifetime
	if lifetime == 0 {
		lifetime = defaultTokenLifetime
	}

	cacheName = strings.ToLower(cacheName)

	// Pre-build the stable base query parameters (Action and User). X-Amz-Expires
	// and the SigV4 parameters are added per-call in Token().
	baseQuery := url.Values{}
	baseQuery.Set("Action", "connect")
	baseQuery.Set("User", user)
	if opt.Serverless {
		// ElastiCache Serverless requires ResourceType=ServerlessCache in the signed
		// token URL; without it the cluster rejects the connection with WRONGPASS.
		baseQuery.Set("ResourceType", "ServerlessCache")
	}

	return &tokenProvider{
		creds:     cfg.Credentials,
		signer:    v4.NewSigner(),
		endpoint:  fmt.Sprintf("https://%s/", cacheName),
		baseQuery: baseQuery,
		region:    region,
		expires:   lifetime,
	}, nil
}

// Token generates a fresh presigned authentication token. It is safe to call
// concurrently because it builds a new http.Request each time and does not
// mutate any shared state.
func (p *tokenProvider) Token(ctx context.Context) (string, error) {
	creds, err := p.creds.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("awsredisauth: retrieving credentials: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, p.endpoint, nil)
	if err != nil {
		// Should never happen for a valid endpoint built in NewTokenProvider.
		return "", fmt.Errorf("awsredisauth: building request: %w", err)
	}

	// Clone the base query and add X-Amz-Expires before signing.
	q := url.Values{}
	for k, v := range p.baseQuery {
		q[k] = v
	}
	q.Set("X-Amz-Expires", strconv.Itoa(int(p.expires.Seconds())))
	req.URL.RawQuery = q.Encode()

	signedURI, _, err := p.signer.PresignHTTP(
		ctx,
		creds,
		req,
		emptyPayloadHash,
		elasticacheSigningService,
		p.region,
		time.Now().UTC(),
	)
	if err != nil {
		return "", fmt.Errorf("awsredisauth: presigning request: %w", err)
	}

	// Strip the scheme. ElastiCache expects the token to be the presigned URL
	// without the "https://" prefix.
	token := strings.TrimPrefix(signedURI, "https://")
	token = strings.TrimPrefix(token, "http://")
	return token, nil
}
