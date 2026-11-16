package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

// The shared strategy's own tests cover its curves. These cover what the policy adds: the
// classes a session reports, the advised floor, and the reset on a healthy connection.

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// testPolicy has no strategy jitter, so the curve is exact, and a clock the test moves.
func testPolicy() (*reconnectPolicy, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	policy := newReconnectPolicy(
		retry.WithJitter(func(time.Duration) time.Duration { return 0 }),
		retry.WithClock(clock.now))
	return policy, clock
}

func nextDelay(t *testing.T, policy *reconnectPolicy) time.Duration {
	t.Helper()
	delay, _ := policy.next()
	return delay
}

// An advised delay is a minimum, so jitter only lengthens it.
func withinAdviceJitter(t *testing.T, base, got time.Duration) {
	t.Helper()
	assert.GreaterOrEqual(t, got, base, "an advised delay is never shortened")
	assert.LessOrEqual(t, got, base+time.Duration(float64(base)*adviceJitterRatio),
		"jitter adds at most its ratio")
}

func TestReconnectPolicyBacksOffToTheNormalCeiling(t *testing.T) {
	policy, _ := testPolicy()
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		assert.Equal(t, want*time.Second, nextDelay(t, policy))
	}
}

// The real strategy jitters downward by up to half, never upward.
func TestReconnectPolicyJittersWithinHalfTheDelay(t *testing.T) {
	policy := newReconnectPolicy()
	for range 6 {
		policy.next()
	}
	for range 20 {
		delay := nextDelay(t, policy)
		assert.GreaterOrEqual(t, delay, retryNormalCeiling/2)
		assert.LessOrEqual(t, delay, retryNormalCeiling)
	}
}

// A connection that fails soon after its handshake keeps backing off.
func TestReconnectPolicyResetsOnlyAfterAHealthyPeriod(t *testing.T) {
	policy, clock := testPolicy()
	for range 4 {
		policy.next()
	}

	policy.healthy()
	clock.t = clock.t.Add(retryResetThreshold - time.Second)
	policy.healthy()
	assert.Equal(t, 16*time.Second, nextDelay(t, policy), "a short connection does not reset")

	policy.healthy()
	clock.t = clock.t.Add(retryResetThreshold)
	policy.healthy()
	assert.Equal(t, 1*time.Second, nextDelay(t, policy))
}

func TestReconnectPolicyHonorsAdviceAsAFloor(t *testing.T) {
	policy, _ := testPolicy()
	policy.advise(5 * time.Second)
	withinAdviceJitter(t, 5*time.Second, nextDelay(t, policy))

	// Advice applies to the next reconnect, not to every later one.
	assert.Equal(t, 2*time.Second, nextDelay(t, policy))
}

// Advice shorter than the schedule already computed must not shorten it.
func TestReconnectPolicyIgnoresAdviceBelowTheSchedule(t *testing.T) {
	policy, _ := testPolicy()
	for range 6 {
		policy.next()
	}
	policy.advise(1 * time.Millisecond)
	assert.Equal(t, retryNormalCeiling, nextDelay(t, policy))
}

// Without the cap, one bad advised value could park a whole fleet on stale flags.
func TestReconnectPolicyClampsAdviceToAnHour(t *testing.T) {
	policy, _ := testPolicy()
	policy.advise(72 * time.Hour)
	withinAdviceJitter(t, maxBackoffAdvice, nextDelay(t, policy))
}

func TestReconnectPolicyIgnoresNonPositiveAdvice(t *testing.T) {
	policy, _ := testPolicy()
	policy.advise(0)
	policy.advise(-1 * time.Second)
	assert.Equal(t, 1*time.Second, nextDelay(t, policy))
}

// An unexpected failure needs an operator, so the relay moves to the extended curve and stays
// there until a healthy period resets it.
func TestReconnectPolicyMovesToTheExtendedCurveOnAnUnexpectedFailure(t *testing.T) {
	policy, _ := testPolicy()
	policy.unexpected()
	delay, extended := policy.next()
	require.True(t, extended, "the first unexpected failure reports the change")
	assert.Equal(t, retryExtendedInitialDelay, delay)

	// A later normal failure stays on the extended curve, and the class is per failure.
	delay, extended = policy.next()
	assert.False(t, extended)
	assert.Equal(t, 2*retryExtendedInitialDelay, delay)

	for range 10 {
		policy.next()
	}
	assert.Equal(t, retryExtendedCeiling, nextDelay(t, policy))
}

func TestReconnectPolicyTakesTheLongerOfAdviceAndTheExtendedCurve(t *testing.T) {
	policy, _ := testPolicy()
	policy.advise(10 * time.Second)
	policy.unexpected()
	assert.Equal(t, retryExtendedInitialDelay, nextDelay(t, policy))
}
