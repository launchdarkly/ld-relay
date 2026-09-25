package session

import (
	"math/rand/v2"
	"time"
)

const (
	// baseReconnectDelay is the delay before the first retry after a failure.
	baseReconnectDelay = 1 * time.Second

	// maxReconnectDelay bounds the ordinary exponential schedule.
	maxReconnectDelay = 30 * time.Second

	// maxBackoffAdvice caps what the relay honors from a server's close advice. Without the
	// cap, one bad advised value could park a whole fleet on stale flags indefinitely.
	maxBackoffAdvice = 1 * time.Hour

	// configErrorDelay is the floor after a close that reports a configuration problem.
	// None of those resolves until an operator edits the configuration, so retrying on the
	// ordinary schedule only produces load and noise.
	configErrorDelay = 1 * time.Hour

	// jitterRatio is the largest fraction of a delay that jitter adds to it.
	jitterRatio = 0.5

	// maxAttemptShift bounds the exponent so the doubling cannot overflow. The ordinary
	// ceiling is reached long before this.
	maxAttemptShift = 8
)

// reconnectPolicy decides how long to wait before each connection attempt.
//
// Every delay carries jitter. A server deploy disconnects a whole fleet at once, and an
// unjittered schedule brings that fleet back as a herd. A floor raises a delay but never
// removes the jitter above it, which is why jitter applies last.
type reconnectPolicy struct {
	attempts int
	floor    time.Duration
}

// advise records the minimum delay a server asked for, ignoring a non-positive value and
// clamping anything longer than the cap.
func (p *reconnectPolicy) advise(delay time.Duration) {
	if delay <= 0 {
		return
	}
	if delay > maxBackoffAdvice {
		delay = maxBackoffAdvice
	}
	if delay > p.floor {
		p.floor = delay
	}
}

// park raises the floor to the configuration-error delay.
func (p *reconnectPolicy) park() {
	if configErrorDelay > p.floor {
		p.floor = configErrorDelay
	}
}

// next returns the delay before the next attempt and advances the schedule. It consumes any
// floor, because close advice applies to the next reconnect rather than to all of them.
func (p *reconnectPolicy) next() time.Duration {
	delay := baseReconnectDelay << p.attempts
	if delay > maxReconnectDelay {
		delay = maxReconnectDelay
	}
	if p.attempts < maxAttemptShift {
		p.attempts++
	}
	if delay < p.floor {
		delay = p.floor
	}
	p.floor = 0
	return withJitter(delay)
}

// reset returns the schedule to its start. The session calls it once a handshake succeeds.
func (p *reconnectPolicy) reset() {
	p.attempts = 0
	p.floor = 0
}

func withJitter(delay time.Duration) time.Duration {
	spread := int64(float64(delay) * jitterRatio)
	if spread <= 0 {
		return delay
	}
	// Jitter spreads a fleet's reconnects. It is not a security decision, so the fast
	// generator is the right one.
	return delay + time.Duration(rand.Int64N(spread)) //nolint:gosec // see above
}
