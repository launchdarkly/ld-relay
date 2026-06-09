package awsredisauth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestJitteredMaxConnAge_InRange verifies that JitteredMaxConnAge always returns a
// duration in the expected range [10h30m, 11h].
func TestJitteredMaxConnAge_InRange(t *testing.T) {
	const iterations = 10_000
	const lower = maxConnAgeBase - maxConnAgeJitter // 10h30m
	const upper = maxConnAgeBase                    // 11h

	for i := 0; i < iterations; i++ {
		d := JitteredMaxConnAge()
		assert.GreaterOrEqualf(t, d, lower,
			"iteration %d: got %v, want >= %v", i, d, lower)
		assert.LessOrEqualf(t, d, upper,
			"iteration %d: got %v, want <= %v", i, d, upper)
	}
}

// TestJitteredMaxConnAge_Spread verifies that consecutive calls produce some variation,
// confirming the jitter is actually random. With 1000 calls and a 30-minute range in
// nanoseconds (~1.8e12), the probability that all values are identical is astronomically small.
func TestJitteredMaxConnAge_Spread(t *testing.T) {
	const iterations = 1000
	seen := make(map[time.Duration]struct{}, iterations)
	for i := 0; i < iterations; i++ {
		seen[JitteredMaxConnAge()] = struct{}{}
	}
	// With 1000 draws from a ~1.8e12-value range, expect many distinct values.
	// Requiring at least 2 is a minimal sanity check.
	assert.Greater(t, len(seen), 1, "JitteredMaxConnAge should produce multiple distinct values")
}
