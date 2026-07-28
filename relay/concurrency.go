package relay

import (
	"log/slog"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
)

const defaultInitSendTimeout = 30 * time.Second

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

	perEnvPct := c.PerEnvMaxPercent.GetOrElse(0)
	if perEnvPct < 0 {
		perEnvPct = 0
	}
	if perEnvPct > 100 {
		perEnvPct = 100
	}
	// Translate the per-environment percentage into an absolute participation cap over
	// the total budget (held + queued). Only meaningful when the limiter is enabled.
	perEnvMax := 0
	if perEnvPct > 0 && maxConcurrent > 0 {
		perEnvMax = perEnvPct * (maxConcurrent + maxQueued) / 100
		if perEnvMax < 1 {
			perEnvMax = 1
		}
	}

	return initConcurrency{
		limiter: concurrency.New("init_delivery", concurrency.Params{
			MaxConcurrent: maxConcurrent,
			MaxQueued:     maxQueued,
			PerEnvMax:     perEnvMax,
		}),
		sendTimeout: c.SendTimeout.GetOrElse(defaultInitSendTimeout),
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
