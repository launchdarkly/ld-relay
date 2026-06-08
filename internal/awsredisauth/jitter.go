package awsredisauth

import (
	"math/rand/v2"
	"time"
)

const (
	// maxConnAgeBase is the target maximum connection age for ElastiCache IAM-auth
	// pools. AWS enforces a hard 12-hour ceiling on IAM-authenticated connections,
	// so we recycle well before that limit.
	maxConnAgeBase = 11 * time.Hour

	// maxConnAgeJitter is the maximum amount subtracted from maxConnAgeBase to
	// produce a per-pool jittered value. This staggers reconnects across pods and
	// across the three Relay pools within a single pod. The effective range is
	// [maxConnAgeBase - maxConnAgeJitter, maxConnAgeBase] = [10h30m, 11h].
	maxConnAgeJitter = 30 * time.Minute
)

// JitteredMaxConnAge returns a uniformly random connection max-age in the range
// [10h30m, 11h]. Callers should invoke it once per pool construction; each pool
// may (and should) get its own independent value.
//
// # Why jitter?
//
// All three Relay Redis pools use IAM authentication, which means every connection
// must be recycled before the AWS-enforced 12-hour IAM token lifetime. Without
// jitter, every connection in every pool would expire at the same wall-clock time,
// causing a thundering-herd reconnect storm against ElastiCache.
//
// This per-pool jitter staggers reconnects in two dimensions:
//
//   - Across pods: different pods draw independent random values, so their recycle
//     windows are spread across the 30-minute jitter band.
//   - Across pools within a pod: the three pools (SDK data store, big-segments store,
//     auto-config cache) each call JitteredMaxConnAge independently and will almost
//     always draw different values.
//
// # Residual thundering-herd (acknowledged limitation)
//
// This single-per-pool duration does NOT stagger connections within a single pool
// that were created at the same time. For example, if a Redis failover triggers
// simultaneous reconnects of all idle connections in the pool, they will all receive
// the same MaxConnAge value and therefore all expire together ~11h later, causing
// another burst at that time.
//
// True within-pool staggering requires per-connection max-age tracking, which
// neither go-redis/v8 nor redigo supports via a single duration. This residual is
// resolved by the planned go-redis v9 migration, which provides a native
// CredentialsProvider interface that re-auths in place on each connection refresh
// instead of forcing a full TCP recycle.
func JitteredMaxConnAge() time.Duration {
	jitter := time.Duration(rand.Int64N(int64(maxConnAgeJitter))) //nolint:gosec // connection-age jitter is not security-sensitive; a weak RNG is fine
	return maxConnAgeBase - jitter
}
