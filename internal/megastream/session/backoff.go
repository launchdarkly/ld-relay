package session

import (
	"math/rand/v2"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

const (
	// retryInitialDelay is the base delay of the normal curve.
	retryInitialDelay = 1 * time.Second

	// retryNormalCeiling bounds the normal curve.
	retryNormalCeiling = 30 * time.Second

	// retryExtendedInitialDelay is the first delay after an unexpected failure, such as a
	// close that reports a configuration problem. None of those resolves until an operator
	// edits the configuration, so retrying on the normal curve only produces load and noise.
	retryExtendedInitialDelay = 5 * time.Minute

	// retryExtendedCeiling bounds the extended curve.
	retryExtendedCeiling = 1 * time.Hour

	// retryResetThreshold is how long a connection must stay up before the schedule returns
	// to its start. A connection that fails soon after its handshake keeps backing off, so
	// a server that accepts and then drops the relay does not get a reconnect every second.
	retryResetThreshold = 60 * time.Second

	// maxBackoffAdvice caps what the relay honors from a server's close advice. Without the
	// cap, one bad advised value could park a whole fleet on stale flags indefinitely.
	maxBackoffAdvice = 1 * time.Hour

	// adviceJitterRatio is the largest fraction of an advised delay that jitter adds to it.
	adviceJitterRatio = 0.5
)

// reconnectPolicy decides how long to wait before each connection attempt.
//
// The schedule is the shared retry strategy. This type adds the one thing MegaStream has
// that the strategy does not: a server can advise a minimum delay when it closes the
// session.
type reconnectPolicy struct {
	strategy *retry.Strategy

	// class is the class of the failure in progress. A close or a refused dial can mark the
	// failure unexpected before the connection attempt returns.
	class retry.FailureClass

	// floor is the minimum delay a server advised for the next reconnect.
	floor time.Duration
}

func newReconnectPolicy(options ...retry.Option) *reconnectPolicy {
	return &reconnectPolicy{strategy: retry.NewStrategy(retry.Config{
		InitialDelay:         retryInitialDelay,
		NormalCeiling:        retryNormalCeiling,
		ExtendedInitialDelay: retryExtendedInitialDelay,
		ExtendedCeiling:      retryExtendedCeiling,
		ResetThreshold:       retryResetThreshold,
	}, options...)}
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

// unexpected marks the failure in progress as one that is unlikely to correct itself soon.
func (p *reconnectPolicy) unexpected() {
	p.class = retry.Unexpected
}

// healthy records that the connection is serving. The session reports this on every frame
// it reads, and the schedule returns to its start once the connection has stayed up for the
// reset threshold.
func (p *reconnectPolicy) healthy() {
	p.strategy.OnHealthy()
}

// next records the failure that ended the last attempt and returns the delay before the next
// one. The second result reports whether this failure moved the schedule onto the extended
// curve. It consumes the failure class and any floor, because both describe one close
// rather than all later ones.
func (p *reconnectPolicy) next() (time.Duration, bool) {
	engaged := p.strategy.OnFailure(p.class)
	delay := p.strategy.NextWait()
	if delay < p.floor {
		// The strategy's jitter shortens a delay, and an advised delay is a minimum, so
		// jitter here lengthens it instead. Without jitter, a fleet the server closed at
		// once would come back at once.
		delay = withJitter(p.floor)
	}
	p.class = retry.Normal
	p.floor = 0
	return delay, engaged
}

func withJitter(delay time.Duration) time.Duration {
	spread := int64(float64(delay) * adviceJitterRatio)
	if spread <= 0 {
		return delay
	}
	// Jitter spreads a fleet's reconnects. It is not a security decision, so the fast
	// generator is the right one.
	return delay + time.Duration(rand.Int64N(spread)) //nolint:gosec // see above
}
