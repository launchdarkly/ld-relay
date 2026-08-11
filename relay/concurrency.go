package relay

import (
	"log/slog"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

// defaultInitSendTimeout is the absolute cap on how long a single gated delivery may hold a
// slot. It is generous because the throughput floor (see internal/initwrite) does the
// fast-stall detection; this only backstops a client that stays right at the floor on a very
// large payload.
const defaultInitSendTimeout = 2 * time.Minute

const (
	// maxInitMaxConcurrent bounds the slot count. Each slot represents one resident full
	// payload, and the limiter fills one token per slot at construction, so an absurd value
	// (for example 2^31) stalls startup for close to a minute and promises memory the host
	// cannot have. Values above this are treated as misconfiguration and clamped.
	maxInitMaxConcurrent = 65536
	// maxInitMaxQueued bounds the queue. Each queued request holds an open connection and
	// its goroutines while it waits, so a bound far beyond any real client population only
	// hides a misconfiguration.
	maxInitMaxQueued = 1_000_000
	// minInitSendTimeout is the smallest usable delivery cap. The write deadline adds
	// initwrite.WriteSlack per write; a cap below that expires before even a small first
	// write, which would cut every delivery.
	minInitSendTimeout = initwrite.WriteSlack
)

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
// nonsensical values rather than failing so a bad edit can't crashloop the Relay. Each
// clamp logs a warning with the value it applied. A MaxConcurrent that is unset or <=0
// yields a disabled (pass-through) limiter.
func newInitConcurrency(c config.ConcurrencyConfig, log *slog.Logger) initConcurrency {
	maxConcurrent := c.MaxConcurrent.GetOrElse(0)
	if maxConcurrent > maxInitMaxConcurrent {
		log.Warn("INIT_MAX_CONCURRENT is beyond the supported range; clamping",
			"configured", maxConcurrent, "effective", maxInitMaxConcurrent)
		maxConcurrent = maxInitMaxConcurrent
	}

	maxQueued := c.MaxQueued.GetOrElse(0)
	if maxQueued < 0 {
		log.Warn("INIT_MAX_QUEUED is negative; treating it as 0 (no queue)",
			"configured", maxQueued)
		maxQueued = 0
	}
	if maxQueued > maxInitMaxQueued {
		log.Warn("INIT_MAX_QUEUED is beyond the supported range; clamping",
			"configured", maxQueued, "effective", maxInitMaxQueued)
		maxQueued = maxInitMaxQueued
	}

	// A zero or negative timeout (unset, or explicitly 0) falls back to the default so both
	// the poll and stream paths get a consistent, non-zero deadline rather than none. A tiny
	// timeout is clamped up: below the writer's per-write slack it would cut every delivery.
	sendTimeout := c.SendTimeout.GetOrElse(defaultInitSendTimeout)
	if sendTimeout <= 0 {
		sendTimeout = defaultInitSendTimeout
	} else if sendTimeout < minInitSendTimeout {
		log.Warn("INIT_SEND_TIMEOUT is too small to deliver anything; clamping",
			"configured", sendTimeout, "effective", minInitSendTimeout)
		sendTimeout = minInitSendTimeout
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
