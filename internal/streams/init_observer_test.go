package streams

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"net/http/httptest"

	"github.com/launchdarkly/eventsource"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakeInitObserver records every call, safely across goroutines.
type fakeInitObserver struct {
	mu         sync.Mutex
	calls      []string
	deadlineNs int64
}

func (f *fakeInitObserver) RecordDelivery(protocol, outcome string, capEngaged bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("delivery:%s:%s:%v", protocol, outcome, capEngaged))
}
func (f *fakeInitObserver) RecordUpToDate(afterWait bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("up_to_date:%v", afterWait))
}
func (f *fakeInitObserver) RecordShed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "shed")
}
func (f *fakeInitObserver) AddDeadlineSetErrors(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadlineNs += n
}
func (f *fakeInitObserver) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newObserverRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func observerStore(t *testing.T, state int) *mockStoreQueries {
	t.Helper()
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		flag := ldbuilders.NewFlagBuilder("flagkey").Version(state).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: "flagkey", Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector("s"+strconv.Itoa(state), state), nil
	})
	return store
}

func waitForCalls(t *testing.T, f *fakeInitObserver, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := f.snapshot()
		if len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "observer saw only %v", calls)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamReplayReportsToInitObserver(t *testing.T) {
	t.Run("shed", func(t *testing.T) {
		obs := &fakeInitObserver{}
		limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
		hold, _ := limiter.Acquire(context.Background())
		defer hold()
		repo := &serverSideEnvStreamRepository{store: observerStore(t, 1), logger: slog.Default(), isV2: true, initLimiter: limiter, initObserver: obs}
		drainReplay(t, repo.ReplayWithContext(context.Background(), "", "s9"))
		assert.Equal(t, []string{"shed"}, waitForCalls(t, obs, 1))
	})

	t.Run("up-to-date without a wait", func(t *testing.T) {
		obs := &fakeInitObserver{}
		limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
		repo := &serverSideEnvStreamRepository{store: observerStore(t, 2), logger: slog.Default(), isV2: true, initLimiter: limiter, initObserver: obs}
		drainReplay(t, repo.ReplayWithContext(context.Background(), "", "s2"))
		assert.Equal(t, []string{"up_to_date:false"}, waitForCalls(t, obs, 1))
	})

	t.Run("completed and connection_ended", func(t *testing.T) {
		obs := &fakeInitObserver{}
		limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 2, MaxQueued: 1})
		repo := &serverSideEnvStreamRepository{store: observerStore(t, 1), logger: slog.Default(), isV2: true, initLimiter: limiter, initObserver: obs}

		// Completed: the test plays the handler role and flushes at end of batch.
		iw := initwrite.WrapGated(newObserverRecorder(), 30*time.Second)
		ctx := context.WithValue(context.Background(), initWriterKey{}, iw)
		drainReplay(t, repo.ReplayWithContext(ctx, "", "s9"))
		iw.Flush()
		calls := waitForCalls(t, obs, 1)
		assert.Equal(t, "delivery:fdv2:completed:false", calls[0])

		// Connection ended: the client goes away instead of the flush arriving.
		iw2 := initwrite.WrapGated(newObserverRecorder(), 30*time.Second)
		cctx, cancel := context.WithCancel(context.Background())
		cctx = context.WithValue(cctx, initWriterKey{}, iw2)
		drainReplay(t, repo.ReplayWithContext(cctx, "", "s9"))
		cancel()
		calls = waitForCalls(t, obs, 2)
		assert.Equal(t, "delivery:fdv2:connection_ended:false", calls[1])
	})
}

func TestStreamReplayDeliverySpanEndsOnceWithOutcome(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	defer otel.SetTracerProvider(prev)

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 2, MaxQueued: 1})
	repo := &serverSideEnvStreamRepository{store: observerStore(t, 1), logger: slog.Default(), isV2: true, initLimiter: limiter}

	// A completed delivery: exactly one delivery span, with the completed outcome.
	pctx, parent := tracing.Tracer().Start(context.Background(), "parent")
	iw := initwrite.WrapGated(newObserverRecorder(), 30*time.Second)
	pctx = context.WithValue(pctx, initWriterKey{}, iw)
	drainReplay(t, repo.ReplayWithContext(pctx, "", "s9"))
	iw.Flush()
	waitForSpan(t, sr, tracing.SpanInitDelivery)
	spans := endedSpans(sr, tracing.SpanInitDelivery)
	require.Len(t, spans, 1, "the delivery span must end exactly one time")
	assert.Contains(t, attrString(spans[0]), "completed")
	parent.End()

	// A shed: no delivery span, and the shed event lands on the request span. A separate
	// limiter with no queue, its only slot held, makes the shed deterministic.
	shedLimiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	hold, _ := shedLimiter.Acquire(context.Background())
	defer hold()
	sctx, sparent := tracing.Tracer().Start(context.Background(), "shed-parent")
	repo2 := &serverSideEnvStreamRepository{store: observerStore(t, 1), logger: slog.Default(), isV2: true, initLimiter: shedLimiter}
	drainReplay(t, repo2.ReplayWithContext(sctx, "", "s9"))
	sparent.End()
	assert.Len(t, endedSpans(sr, tracing.SpanInitDelivery), 1, "a shed must not create a delivery span")
	found := false
	for _, s := range sr.Ended() {
		if s.Name() == "shed-parent" {
			for _, e := range s.Events() {
				if e.Name == tracing.EventInitShed {
					found = true
				}
			}
		}
	}
	assert.True(t, found, "the shed event must land on the request span")
}

func TestStreamReplayUpToDateAfterWaitEventLandsOnTheRequestSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	defer otel.SetTracerProvider(prev)

	// The store starts behind the client's basis and catches up while the client waits in
	// the queue, so the post-admission read takes the up-to-date path.
	var stateNow atomic.Int64
	stateNow.Store(1)
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		st := int(stateNow.Load())
		flag := ldbuilders.NewFlagBuilder("flagkey").Version(st).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: "flagkey", Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector("s"+strconv.Itoa(st), st), nil
	})

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	hold, ok := limiter.Acquire(context.Background())
	require.True(t, ok)

	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true, initLimiter: limiter}
	pctx, parent := tracing.Tracer().Start(context.Background(), "request-parent")
	eventCh := repo.ReplayWithContext(pctx, "", "s2")
	deadline := time.Now().Add(2 * time.Second)
	for limiter.Stats().Waiting != 1 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "client never queued (stats: %+v)", limiter.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	stateNow.Store(2)
	hold()
	drainReplay(t, eventCh)
	parent.End()

	// The delivery span ends with the up_to_date outcome, but the event itself belongs on
	// the request span, the same as the no-wait path and the documentation.
	waitForSpan(t, sr, tracing.SpanInitDelivery)
	dspans := endedSpans(sr, tracing.SpanInitDelivery)
	require.Len(t, dspans, 1)
	assert.Contains(t, attrString(dspans[0]), "up_to_date")
	for _, e := range dspans[0].Events() {
		assert.NotEqual(t, tracing.EventInitUpToDate, e.Name, "the event must not land on the delivery span")
	}
	found := false
	for _, s := range sr.Ended() {
		if s.Name() == "request-parent" {
			for _, e := range s.Events() {
				if e.Name == tracing.EventInitUpToDate {
					found = true
				}
			}
		}
	}
	assert.True(t, found, "the up-to-date event must land on the request span")
}

func drainReplay(t *testing.T, ch <-chan eventsource.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-deadline:
			require.Fail(t, "timed out draining the replay")
		}
	}
}

func endedSpans(sr *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

func waitForSpan(t *testing.T, sr *tracetest.SpanRecorder, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(endedSpans(sr, name)) == 0 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "span %s never ended", name)
		}
		time.Sleep(time.Millisecond)
	}
}

func attrString(s sdktrace.ReadOnlySpan) string {
	out := ""
	for _, kv := range s.Attributes() {
		out += string(kv.Key) + "=" + kv.Value.Emit() + " "
	}
	return out
}
