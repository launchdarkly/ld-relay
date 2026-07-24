package relay

import (
	"log/slog"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
)

const defaultStreamPutSendTimeout = 30 * time.Second

// concurrencyLimiters holds Relay's admission limiters. Polls and full-basis
// stream replays share ONE "basis delivery" budget, since they contend for the
// same CPU (serialization), memory, and egress bandwidth; up-to-date stream
// replays and deltas are cheap and are not gated.
type concurrencyLimiters struct {
	basisDelivery        *concurrency.Limiter
	streamPutSendTimeout time.Duration
}

func newConcurrencyLimiters(c config.ConcurrencyConfig) concurrencyLimiters {
	maxConcurrent := c.BasisDeliveryMaxConcurrent.GetOrElse(0)
	maxQueued := c.BasisDeliveryMaxQueued.GetOrElse(0)
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
	perEnvMax := 0
	if perEnvPct > 0 && maxConcurrent > 0 {
		perEnvMax = perEnvPct * (maxConcurrent + maxQueued) / 100
		if perEnvMax < 1 {
			perEnvMax = 1
		}
	}

	return concurrencyLimiters{
		basisDelivery: concurrency.New("basis_delivery", concurrency.Params{
			MaxConcurrent: maxConcurrent,
			MaxQueued:     maxQueued,
			PerEnvMax:     perEnvMax,
		}),
		streamPutSendTimeout: c.StreamPutSendTimeout.GetOrElse(defaultStreamPutSendTimeout),
	}
}

func (cl concurrencyLimiters) close() {
	cl.basisDelivery.Close()
}

// logEnabled emits an info line when the shared budget is active.
func (cl concurrencyLimiters) logEnabled(log *slog.Logger) {
	if cl.basisDelivery.Enabled() {
		s := cl.basisDelivery.Stats()
		log.Info("basis-delivery concurrency limit enabled (shared by polls + full-basis streams)",
			"maxConcurrent", s.MaxConcurrent, "maxQueued", s.MaxQueued,
			"streamPutSendTimeout", cl.streamPutSendTimeout)
	}
}
