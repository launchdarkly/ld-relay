package streams

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v4/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldbuilders"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBudgetTestRepo(limiter *concurrency.Limiter) *serverSideEnvStreamRepository {
	flag := ldbuilders.NewFlagBuilder("budget-test-flag").Version(1).
		SingleVariation(ldvalue.Bool(true)).Build()
	store := simpleMockStore{
		initialized: true,
		flags:       []ldstoretypes.KeyedItemDescriptor{{Key: flag.Key, Item: sharedtest.FlagDesc(flag)}},
	}
	return &serverSideEnvStreamRepository{
		store: store, logger: slog.Default(), isV2: true,
		basisLimiter: limiter, envKey: "test-env",
	}
}

func recvEventWithin(t *testing.T, ch <-chan eventsource.Event, timeout time.Duration) (eventsource.Event, bool) {
	t.Helper()
	select {
	case ev, open := <-ch:
		return ev, open
	case <-time.After(timeout):
		t.Fatal("timed out waiting on replay channel")
		return nil, false
	}
}

// A replay that cannot even enter the budget's backlog must close the subscriber's
// connection (via the CloseSubscription sentinel) so the SDK reconnects and retries,
// rather than ending the batch silently and leaving the SDK on an empty stream.
func TestReplayClosesSubscriberWhenBudgetBacklogFull(t *testing.T) {
	limiter := concurrency.New("test", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	t.Cleanup(limiter.Close)
	release, ok := limiter.Acquire(context.Background(), "other")
	require.True(t, ok)
	defer release()

	repo := newBudgetTestRepo(limiter)
	ch := repo.ReplayWithContext(context.Background(), "", "")

	ev, open := recvEventWithin(t, ch, 5*time.Second)
	require.True(t, open)
	assert.Equal(t, eventsource.CloseSubscription, ev)

	_, open = recvEventWithin(t, ch, 5*time.Second)
	assert.False(t, open, "channel should be closed after the sentinel")
}

// A subscriber that disconnects while its replay is queued for a budget slot must
// leave the backlog quietly: the channel closes without any events.
func TestReplayAbandonsQueueWhenSubscriberDisconnects(t *testing.T) {
	limiter := concurrency.New("test", concurrency.Params{MaxConcurrent: 1, MaxQueued: 5})
	t.Cleanup(limiter.Close)
	release, ok := limiter.Acquire(context.Background(), "other")
	require.True(t, ok)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	repo := newBudgetTestRepo(limiter)
	ch := repo.ReplayWithContext(ctx, "", "")

	cancel()

	ev, open := recvEventWithin(t, ch, 5*time.Second)
	assert.False(t, open, "channel should close without events, got %v", ev)
}

// A replay that fits in the backlog waits (rather than shedding) and is served once a
// slot frees up.
func TestReplayWaitsInBacklogForSlot(t *testing.T) {
	limiter := concurrency.New("test", concurrency.Params{MaxConcurrent: 1, MaxQueued: 5})
	t.Cleanup(limiter.Close)
	release, ok := limiter.Acquire(context.Background(), "other")
	require.True(t, ok)

	repo := newBudgetTestRepo(limiter)
	ch := repo.ReplayWithContext(context.Background(), "", "")

	select {
	case ev := <-ch:
		t.Fatalf("replay should still be waiting for a slot, got %v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	release()

	ev, open := recvEventWithin(t, ch, 5*time.Second)
	require.True(t, open)
	assert.NotEqual(t, eventsource.CloseSubscription, ev, "replay should deliver data, not a close")
	assert.NotEmpty(t, ev.Data())
}
