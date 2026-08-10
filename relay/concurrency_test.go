package relay

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingHandler struct{ recs *[]slog.Record }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.recs = append(*h.recs, r)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

func warnCount(recs []slog.Record) int {
	n := 0
	for _, r := range recs {
		if r.Level == slog.LevelWarn {
			n++
		}
	}
	return n
}

func optInt(v int) ct.OptInt { return ct.NewOptInt(v) }

func optDuration(v time.Duration) ct.OptDuration { return ct.NewOptDuration(v) }

func TestInitConcurrencyInRangeValuesPassThrough(t *testing.T) {
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(16),
		MaxQueued:     optInt(100),
		SendTimeout:   optDuration(45 * time.Second),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	s := ic.limiter.Stats()
	assert.Equal(t, 16, s.MaxConcurrent)
	assert.Equal(t, 100, s.MaxQueued)
	assert.Equal(t, 45*time.Second, ic.sendTimeout)
	assert.Zero(t, warnCount(recs), "in-range values must not warn")
}

func TestInitConcurrencyUnsetIsDisabledWithDefaults(t *testing.T) {
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.False(t, ic.limiter.Enabled())
	assert.Equal(t, defaultInitSendTimeout, ic.sendTimeout)
	assert.Zero(t, warnCount(recs))
}

func TestInitConcurrencyClampsHugeMaxConcurrent(t *testing.T) {
	// The limiter fills one token per slot at construction, so an absurd slot count would
	// stall startup for close to a minute; the clamp must keep construction fast.
	var recs []slog.Record
	start := time.Now()
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(1 << 30),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.Less(t, time.Since(start), 5*time.Second, "construction must not stall on a huge slot count")
	assert.Equal(t, maxInitMaxConcurrent, ic.limiter.Stats().MaxConcurrent)
	assert.Equal(t, 1, warnCount(recs), "the clamp must be visible in the log")
}

func TestInitConcurrencyClampsHugeMaxQueued(t *testing.T) {
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(4),
		MaxQueued:     optInt(1 << 30),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.Equal(t, maxInitMaxQueued, ic.limiter.Stats().MaxQueued)
	assert.Equal(t, 1, warnCount(recs))
}

func TestInitConcurrencyNegativeMaxQueuedWarnsAndDisablesQueue(t *testing.T) {
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(1),
		MaxQueued:     optInt(-5),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.Equal(t, 0, ic.limiter.Stats().MaxQueued)
	assert.Equal(t, 1, warnCount(recs))

	// With no queue, a second caller is rejected while the slot is held.
	release, ok := ic.limiter.Acquire(context.Background())
	require.True(t, ok)
	defer release()
	if _, ok2 := ic.limiter.Acquire(context.Background()); ok2 {
		t.Fatal("expected rejection with the queue disabled")
	}
}

func TestInitConcurrencyClampsTinySendTimeout(t *testing.T) {
	// A cap below the writer's per-write slack expires before even a small first write and
	// would cut every delivery; the clamp keeps the cap usable.
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(4),
		SendTimeout:   optDuration(time.Millisecond),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.Equal(t, minInitSendTimeout, ic.sendTimeout)
	assert.Equal(t, 1, warnCount(recs))
}

func TestInitConcurrencyZeroSendTimeoutUsesDefaultQuietly(t *testing.T) {
	// Zero means "unset": it takes the default and is not a misconfiguration.
	var recs []slog.Record
	ic := newInitConcurrency(config.ConcurrencyConfig{
		MaxConcurrent: optInt(4),
	}, slog.New(recordingHandler{&recs}))
	defer ic.close()

	assert.Equal(t, defaultInitSendTimeout, ic.sendTimeout)
	assert.Zero(t, warnCount(recs))
}
