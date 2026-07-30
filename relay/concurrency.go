package relay

import (
	"log/slog"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
)

// defaultInitSendTimeout is the absolute cap on how long a single gated delivery may hold a
// slot. It is generous because the throughput floor (see internal/initwrite) does the
// fast-stall detection; this only backstops a client that stays right at the floor on a very
// large payload. It matches the streamer service's message cap.
const defaultInitSendTimeout = 2 * time.Minute

// initConcurrency holds Relay's shared initialization-delivery budget. Every poll and
// every full-basis stream replay -- across FDv1 and FDv2 -- draws from this one
// limiter, because they all contend for the same resident payload memory and egress
// bandwidth. Cheap operations (FDv2 up-to-date replies, deltas, heartbeats, pings) are
// never gated.
type initConcurrency struct {
	limiter     *concurrency.Limiter
	sendTimeout time.Duration
}

// newInitConcurrency builds the shared budget from the [Concurrency] config, clamping
// nonsensical values rather than failing so a bad edit can't crashloop the Relay. A
// MaxConcurrent that is unset or <=0 yields a disabled (pass-through) limiter.
func newInitConcurrency(c config.ConcurrencyConfig) initConcurrency {
	maxConcurrent := c.MaxConcurrent.GetOrElse(0)

	maxQueued := c.MaxQueued.GetOrElse(0)
	if maxQueued < 0 {
		maxQueued = 0
	}

	// A zero or negative timeout (unset, or explicitly 0) falls back to the default so both
	// the poll and stream paths get a consistent, non-zero deadline rather than none.
	sendTimeout := c.SendTimeout.GetOrElse(defaultInitSendTimeout)
	if sendTimeout <= 0 {
		sendTimeout = defaultInitSendTimeout
	}

	return initConcurrency{
		limiter: concurrency.New("init_delivery", concurrency.Params{
			MaxConcurrent: maxConcurrent,
			MaxQueued:     maxQueued,
		}),
		sendTimeout: sendTimeout,
	}
}

func (ic initConcurrency) close() {
	ic.limiter.Close()
}

// logEnabled emits an info line describing the effective budget when it is active.
func (ic initConcurrency) logEnabled(log *slog.Logger) {
	if ic.limiter.Enabled() {
		s := ic.limiter.Stats()
		log.Info("initialization-delivery concurrency limit enabled (shared by polls and full-basis stream replays)",
			"maxConcurrent", s.MaxConcurrent, "maxQueued", s.MaxQueued, "sendTimeout", ic.sendTimeout)
	}
}
