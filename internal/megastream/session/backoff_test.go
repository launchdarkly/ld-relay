package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Jitter makes exact delays untestable, so these assert the bounds the schedule promises.
func withinJitter(t *testing.T, base, got time.Duration) {
	t.Helper()
	assert.GreaterOrEqual(t, got, base, "a delay is never shorter than its base")
	assert.LessOrEqual(t, got, base+time.Duration(float64(base)*jitterRatio),
		"jitter adds at most its ratio")
}

func TestReconnectPolicyBacksOffToACeiling(t *testing.T) {
	policy := &reconnectPolicy{}

	withinJitter(t, 1*time.Second, policy.next())
	withinJitter(t, 2*time.Second, policy.next())
	withinJitter(t, 4*time.Second, policy.next())
	withinJitter(t, 8*time.Second, policy.next())
	withinJitter(t, 16*time.Second, policy.next())

	// From here the schedule holds at the ceiling rather than doubling without bound.
	for range 20 {
		withinJitter(t, maxReconnectDelay, policy.next())
	}
}

func TestReconnectPolicyResetReturnsToTheStart(t *testing.T) {
	policy := &reconnectPolicy{}
	for range 5 {
		policy.next()
	}
	policy.reset()
	withinJitter(t, 1*time.Second, policy.next())
}

func TestReconnectPolicyHonorsAdviceAsAFloor(t *testing.T) {
	policy := &reconnectPolicy{}
	policy.advise(5 * time.Second)
	withinJitter(t, 5*time.Second, policy.next())

	// Advice applies to the next reconnect, not to every later one.
	withinJitter(t, 2*time.Second, policy.next())
}

// Advice shorter than the schedule already computed must not shorten it.
func TestReconnectPolicyIgnoresAdviceBelowTheSchedule(t *testing.T) {
	policy := &reconnectPolicy{}
	for range 6 {
		policy.next()
	}
	policy.advise(1 * time.Millisecond)
	withinJitter(t, maxReconnectDelay, policy.next())
}

// Without the cap, one bad advised value could park a whole fleet on stale flags.
func TestReconnectPolicyClampsAdviceToAnHour(t *testing.T) {
	policy := &reconnectPolicy{}
	policy.advise(72 * time.Hour)
	withinJitter(t, maxBackoffAdvice, policy.next())
}

func TestReconnectPolicyIgnoresNonPositiveAdvice(t *testing.T) {
	policy := &reconnectPolicy{}
	policy.advise(0)
	policy.advise(-1 * time.Second)
	withinJitter(t, 1*time.Second, policy.next())
}

// A configuration problem needs an operator, so the relay waits a long time before asking
// again. Jitter still applies, so a fleet that hits this together does not return as a herd.
func TestReconnectPolicyParksOnConfigurationErrors(t *testing.T) {
	policy := &reconnectPolicy{}
	policy.park()
	delay := policy.next()
	withinJitter(t, configErrorDelay, delay)
	require.Greater(t, delay, configErrorDelay, "the park delay is jittered, not fixed")
}

func TestReconnectPolicyTakesTheLongerOfAdviceAndPark(t *testing.T) {
	policy := &reconnectPolicy{}
	policy.advise(10 * time.Second)
	policy.park()
	withinJitter(t, configErrorDelay, policy.next())
}
