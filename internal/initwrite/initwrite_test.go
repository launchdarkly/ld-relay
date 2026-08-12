package initwrite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a deterministic time source so deadline math can be asserted exactly rather
// than within a wall-clock tolerance.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// deadlineConn is a ResponseWriter that records the write deadlines armed on it and the order
// of arm-vs-write calls, so tests can assert the arming logic without a real socket. It
// forwards Flush to the embedded recorder (so Flushed stays observable) and guards its state
// with a mutex.
type deadlineConn struct {
	*httptest.ResponseRecorder

	mu              sync.Mutex
	deadlines       []time.Time
	events          []string // "arm" / "write" in call order
	setErr          error    // returned from SetWriteDeadline when non-nil
	setCalls        int
	flushes         int
	written         int
	failAfter       int // if >0, a write fails once this many bytes have been written
	clock           *fakeClock
	advancePerWrite time.Duration
	onFlush         func() // invoked inside Flush, to interleave a concurrent state change
}

func newDeadlineConn() *deadlineConn { return &deadlineConn{ResponseRecorder: httptest.NewRecorder()} }

func (d *deadlineConn) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setCalls++
	if d.setErr != nil {
		return d.setErr
	}
	d.events = append(d.events, "arm")
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineConn) recordWrite(n int) (bool, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, "write")
	if d.clock != nil && d.advancePerWrite > 0 {
		d.clock.advance(d.advancePerWrite)
	}
	if d.failAfter > 0 && d.written >= d.failAfter {
		return true, 0
	}
	d.written += n
	return false, n
}

func (d *deadlineConn) Write(p []byte) (int, error) {
	if fail, _ := d.recordWrite(len(p)); fail {
		return 0, io.ErrClosedPipe
	}
	return d.ResponseRecorder.Write(p)
}

func (d *deadlineConn) WriteString(s string) (int, error) {
	if fail, _ := d.recordWrite(len(s)); fail {
		return 0, io.ErrClosedPipe
	}
	return d.ResponseRecorder.WriteString(s)
}

func (d *deadlineConn) Flush() {
	d.mu.Lock()
	d.events = append(d.events, "flush")
	d.flushes++
	hook := d.onFlush
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	d.ResponseRecorder.Flush()
}

func (d *deadlineConn) armed() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.deadlines)
}

func (d *deadlineConn) eventLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events)
}

func (d *deadlineConn) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setCalls
}

func wrapClocked(base *deadlineConn, maxHold time.Duration, gated bool) (*Writer, *fakeClock) {
	c := newFakeClock()
	base.clock = c
	var w *Writer
	if gated {
		w = WrapGated(base, maxHold)
	} else {
		w = Wrap(base, maxHold)
	}
	w.now = c.now
	return w, c
}

func TestWriteArmsFullChunkAtFloorPlusSlack(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, false)

	start := c.now()
	n, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	assert.Equal(t, chunkSize, n)

	armed := base.armed()
	require.Len(t, armed, 1, "a single full chunk should arm exactly one deadline")
	// 1 MiB at 64 KiB/s is 16s; plus 5s slack = 21s. Literal so a weaker/stronger floor or slack fails.
	assert.Equal(t, 21*time.Second, armed[0].Sub(start))
}

func TestWriteSubChunkArmsFloorPlusSlack(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, false)

	start := c.now()
	_, err := w.Write(make([]byte, 64*1024)) // exactly one second's worth at the floor
	require.NoError(t, err)

	armed := base.armed()
	require.Len(t, armed, 1)
	// 64 KiB at 64 KiB/s is 1s; plus 5s slack = 6s. Pins the floor and slack independently.
	assert.Equal(t, 6*time.Second, armed[0].Sub(start))
}

func TestWriteArmsBeforeEachWrite(t *testing.T) {
	base := newDeadlineConn()
	base.advancePerWrite = minExtension // ensure each chunk re-arms
	w, _ := wrapClocked(base, 5*time.Minute, false)

	_, err := w.Write(make([]byte, 2*chunkSize))
	require.NoError(t, err)
	// The deadline for a chunk must be set before that chunk is written, or the first chunk --
	// the one that can block forever -- would carry no deadline.
	assert.Equal(t, []string{"arm", "write", "arm", "write"}, base.eventLog())
}

func TestWriteStringArmsBeforeEachWrite(t *testing.T) {
	// eventsource writes event data with io.WriteString, so the string path is the production
	// path and must arm before each write just like the byte path.
	base := newDeadlineConn()
	base.advancePerWrite = minExtension
	w, _ := wrapClocked(base, 5*time.Minute, false)

	_, err := w.WriteString(string(make([]byte, 2*chunkSize)))
	require.NoError(t, err)
	assert.Equal(t, []string{"arm", "write", "arm", "write"}, base.eventLog())
}

func TestWriteReArmsEachChunk(t *testing.T) {
	base := newDeadlineConn()
	base.advancePerWrite = minExtension
	w, _ := wrapClocked(base, 5*time.Minute, false)

	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Len(t, base.armed(), 3, "expected one arm per 1 MiB chunk")
}

func TestWriteRatchetSuppressesSameValueReArm(t *testing.T) {
	base := newDeadlineConn()
	// No clock advance: every chunk computes the same deadline, so the ratchet should arm once
	// and suppress the rest. (A removed ratchet would arm on every chunk.)
	w, _ := wrapClocked(base, 5*time.Minute, false)

	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Len(t, base.armed(), 1, "identical deadlines must not be re-armed")
}

func TestWriteCapsDeadlineAtMaxHold(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 500*time.Millisecond, false)
	start := c.now()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)

	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, 500*time.Millisecond, armed[0].Sub(start), "deadline should be clamped to msgStart+maxHold")
}

func TestZeroMaxHoldDisablesCap(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 0, false)
	start := c.now()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)

	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, 21*time.Second, armed[0].Sub(start), "with no cap the per-chunk floor governs")
}

func TestArmDoesNotAdvanceOnSetDeadlineFailure(t *testing.T) {
	base := newDeadlineConn()
	base.setErr = errors.New("deadline not supported")
	// No clock advance: with the fix, lastDeadline stays unset on failure, so every chunk retries;
	// with the bug (advance on failure), the ratchet suppresses all but the first attempt.
	w, _ := wrapClocked(base, 2*time.Minute, false)

	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Equal(t, 3, base.calls(), "each chunk should retry SetWriteDeadline after a failure")
}

func TestWriteErrorPropagatesAndStops(t *testing.T) {
	base := newDeadlineConn()
	base.failAfter = chunkSize // fail on the second chunk
	w := Wrap(base, 2*time.Minute)

	n, err := w.Write(make([]byte, 3*chunkSize))
	assert.ErrorIs(t, err, io.ErrClosedPipe, "a failed write must propagate")
	assert.Equal(t, chunkSize, n, "the count reflects only the bytes written before the failure")
}

func TestWriteStringErrorPropagatesAndStops(t *testing.T) {
	base := newDeadlineConn()
	base.failAfter = chunkSize
	w := Wrap(base, 2*time.Minute)

	n, err := w.WriteString(string(make([]byte, 3*chunkSize)))
	assert.ErrorIs(t, err, io.ErrClosedPipe)
	assert.Equal(t, chunkSize, n)
}

func TestWriteStringMatchesWrite(t *testing.T) {
	payload := make([]byte, 3*chunkSize+777)

	byteConn := newDeadlineConn()
	byteConn.advancePerWrite = minExtension
	bw, _ := wrapClocked(byteConn, 2*time.Minute, false)
	nBytes, err := bw.Write(payload)
	require.NoError(t, err)

	strConn := newDeadlineConn()
	strConn.advancePerWrite = minExtension
	sw, _ := wrapClocked(strConn, 2*time.Minute, false)
	nStr, err := sw.WriteString(string(payload))
	require.NoError(t, err)

	assert.Equal(t, nBytes, nStr, "byte and string paths should write the same count")
	assert.Len(t, strConn.armed(), len(byteConn.armed()), "both paths should arm the same number of deadlines")
	assert.Equal(t, byteConn.Body.Bytes(), strConn.Body.Bytes(), "both paths should emit identical bytes")
}

func TestGatedArmsOnlyBetweenBeginAndEndFlush(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)

	// Before Begin: writes pass through unarmed.
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	require.Empty(t, base.armed(), "no deadline should be armed before Begin")

	// During the delivery: writes arm.
	w.Begin()
	start := c.now()
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, 21*time.Second, armed[0].Sub(start))

	// End + the single end-of-batch flush clears the deadline exactly once.
	w.End()
	w.Flush()
	armed = base.armed()
	require.Len(t, armed, 2, "end-of-delivery flush should clear the deadline")
	assert.True(t, armed[1].IsZero(), "the clearing call must set the zero time")

	// After the delivery: writes pass through unarmed again.
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	assert.Len(t, base.armed(), 2, "no new deadline should be armed after the delivery ends")
}

func TestGatedWriteStringLifecycle(t *testing.T) {
	// eventsource writes event data via io.WriteString, so the string path is the production SSE
	// path and must behave like Write under gating.
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)

	_, err := io.WriteString(w, "before begin")
	require.NoError(t, err)
	require.Empty(t, base.armed(), "no deadline before Begin on the string path")

	w.Begin()
	start := c.now()
	_, err = io.WriteString(w, string(make([]byte, chunkSize)))
	require.NoError(t, err)
	require.Len(t, base.armed(), 1)
	assert.Equal(t, 21*time.Second, base.armed()[0].Sub(start))

	w.End()
	w.Flush()
	require.Len(t, base.armed(), 2)
	assert.True(t, base.armed()[1].IsZero(), "string-path delivery should clear at end")
	assert.GreaterOrEqual(t, base.flushes, 1)
}

func TestGatedInertAfterDeliveryAcrossHeartbeats(t *testing.T) {
	base := newDeadlineConn()
	base.advancePerWrite = time.Second
	w, _ := wrapClocked(base, 2*time.Minute, true)

	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	w.End()
	w.Flush()
	armedAtEnd := len(base.armed())

	// Simulate later heartbeat traffic on the persistent stream: with the clock advancing, a
	// writer that failed to deactivate at end-of-delivery would re-arm here and fire on an idle
	// HTTP/2 stream. It must not.
	for range 3 {
		_, err = w.Write([]byte(": keep-alive\n\n"))
		require.NoError(t, err)
		w.Flush()
	}
	assert.Len(t, base.armed(), armedAtEnd, "heartbeats after the delivery must not re-arm the deadline")
}

func TestDoneIsFailSafeClosedOutsideDelivery(t *testing.T) {
	base := newDeadlineConn()
	w := WrapGated(base, time.Minute)

	// With no delivery in progress, Done must already be closed so a waiting producer releases
	// its slot immediately instead of pinning it forever.
	assertClosed(t, w.Done(), "Done before Begin")

	w.Begin()
	assertOpen(t, w.Done(), "Done during delivery")

	w.End()
	w.Flush()
	assertClosed(t, w.Done(), "Done after end-of-delivery flush")
}

func TestWaitAndFinishReturnsWhenDelivered(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	w.End()

	go w.Flush() // the handler goroutine's end-of-batch flush closes Done

	runWithTimeout(t, "WaitAndFinish should return once delivered", func() {
		w.WaitAndFinish(context.Background())
	})
	armed := base.armed()
	require.NotEmpty(t, armed)
	assert.True(t, armed[len(armed)-1].IsZero(), "a delivered stream should have its deadline cleared")
}

func TestWaitAndFinishOnCancelReleasesWithoutTouchingConnection(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms once (setCalls == 1)
	require.NoError(t, err)
	require.Equal(t, 1, base.calls())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runWithTimeout(t, "WaitAndFinish should return on cancel", func() {
		w.WaitAndFinish(ctx)
	})

	// The cancel path must not touch the connection: no additional SetWriteDeadline, and the
	// waiter is released so the slot frees.
	assert.Equal(t, 1, base.calls(), "cancellation must not set the write deadline")
	assertClosed(t, w.Done(), "Done after cancel")
}

// panicDeadlineConn panics if the write deadline is set, to prove the producer-side path never
// touches the connection (which, after the handler returns, would crash inside net/http).
type panicDeadlineConn struct{ *httptest.ResponseRecorder }

func (panicDeadlineConn) SetWriteDeadline(time.Time) error {
	panic("SetWriteDeadline must not be called from the producer path")
}

func TestWaitAndFinishNeverSetsDeadline(t *testing.T) {
	base := panicDeadlineConn{httptest.NewRecorder()}
	w := WrapGated(base, 2*time.Minute)
	w.Begin() // no writes, so no legitimate handler-side arm

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NotPanics(t, func() { w.WaitAndFinish(ctx) },
		"WaitAndFinish must never set the connection deadline")
}

func TestWaitAndFinishAfterHandlerReturnDoesNotCrashHTTP2(t *testing.T) {
	// Reproduce the producer-outlives-handler pattern on a real HTTP/2 connection: a goroutine
	// holds the Writer and calls WaitAndFinish only after the handler has returned and net/http
	// has torn its responseWriter down. Touching the connection there nil-panics inside net/http
	// and crashes the process; WaitAndFinish must not. The handlerReturned signal plus a short
	// settle makes the ordering deterministic, so a reintroduced connection touch crashes reliably
	// (a panic in that goroutine takes the whole test binary down) rather than flaking green.
	finished := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		handlerReturned := make(chan struct{})
		w := WrapGated(rw, 2*time.Minute)
		w.Begin()
		_, _ = w.Write([]byte("data: hi\n\n"))
		w.Flush()
		go func() {
			defer close(finished)
			<-handlerReturned
			time.Sleep(2 * time.Millisecond) // let net/http reclaim the responseWriter
			w.WaitAndFinish(r.Context())
		}()
		defer close(handlerReturned) // fires as the handler returns
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("producer goroutine did not return")
	}
}

func TestFlushWithoutEndDoesNotTeardown(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms one deadline
	require.NoError(t, err)
	require.Len(t, base.armed(), 1)

	// A flush without End -- a mid-delivery/jitter flush, or a producer that errored and skipped
	// End -- must not tear down: the deadline stays armed and the waiter stays parked. (This is
	// the mechanism by which skipping End on an error path would eventually kill a healthy stream.)
	w.Flush()
	assert.Len(t, base.armed(), 1, "flush without End must not clear the deadline")
	assertOpen(t, w.Done(), "done must stay open without End")
}

func TestEndThenFlushTearsDownAnyPartialDelivery(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write([]byte("event: put\n\n")) // only a partial "basis" before abandoning
	require.NoError(t, err)

	// End on the abandon path lets the flush clear the deadline regardless of how much was written.
	w.End()
	w.Flush()
	armed := base.armed()
	require.NotEmpty(t, armed)
	assert.True(t, armed[len(armed)-1].IsZero(), "End+flush clears the deadline even for a partial delivery")
	assertClosed(t, w.Done(), "End+flush releases the waiter")
}

func TestWaitAndFinishCancelAfterDeliveredDoesNotDoubleClose(t *testing.T) {
	// After a clean delivery both select arms are ready, so which branch runs is a coin flip;
	// loop so the ctx branch -- the only one that can double-close -- is certainly exercised.
	for range 200 {
		base := newDeadlineConn()
		w, _ := wrapClocked(base, 2*time.Minute, true)
		w.Begin()
		_, err := w.Write(make([]byte, chunkSize))
		require.NoError(t, err)
		w.End()
		w.Flush() // clean teardown already closed done

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assert.NotPanics(t, func() { w.WaitAndFinish(ctx) })
	}
}

func TestConcurrentAbandonedWaitersDoNotDoubleClose(t *testing.T) {
	// Two waiters parked on the same open done, both released by cancellation: the ctx branch is
	// the only ready arm for both, so both reach the close deterministically and the doneClosed
	// guard is what stops the second from panicking.
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.WaitAndFinish(ctx)
		}()
	}
	time.Sleep(20 * time.Millisecond) // let both park on the open done
	cancel()

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("waiters did not return")
	}
}

func TestEndOfBatchFlushAfterClientGoneDoesNotDoubleClose(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	w.End()

	// The client leaves after End but before the end-of-batch flush: WaitAndFinish's ctx branch
	// closes done first, then the pending flush reaches the teardown on an already-closed
	// channel. The teardown's own guard must keep that from double-closing (a panic here would
	// land on the handler goroutine), and it must still clear the deadline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.WaitAndFinish(ctx)

	assert.NotPanics(t, func() { w.Flush() })
	armed := base.armed()
	require.NotEmpty(t, armed)
	assert.True(t, armed[len(armed)-1].IsZero(), "the flush teardown must still clear the deadline")
}

func TestFlushSamplesStateBeforeFlushing(t *testing.T) {
	// The (active, ending) sample must be taken BEFORE the flush: a delivery that begins (and
	// ends) while the flush is in progress -- the subscribe-time flush racing the producer --
	// belongs to the NEXT flush. A sample taken afterwards would tear it down before its basis
	// is written, freeing the slot and leaving the whole send deadline-less.
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	base.onFlush = func() {
		base.onFlush = nil // once
		w.Begin()
		w.End()
	}
	w.Flush() // entered with no delivery active

	assertOpen(t, w.Done(), "a delivery begun during the flush must not be torn down by it")
	// The delivery is finished by the next flush, as usual.
	w.Flush()
	assertClosed(t, w.Done(), "the next flush finishes the delivery")
}

func TestFlushPanicStillTearsDown(t *testing.T) {
	base := &noFlushConn{}
	w := WrapGated(panicFlushConn{base}, 2*time.Minute)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms
	require.NoError(t, err)
	w.End()

	// The teardown is deferred precisely so a panicking flush cannot strand the delivery: the
	// deadline must be cleared and the waiter released during unwinding, and the panic must
	// still propagate (net/http's per-connection recovery handles it in production).
	assert.Panics(t, func() { w.Flush() }, "the flush panic must propagate")
	require.NotEmpty(t, base.deadlines)
	assert.True(t, base.deadlines[len(base.deadlines)-1].IsZero(), "teardown must clear the deadline during unwinding")
	assertClosed(t, w.Done(), "teardown must release the waiter during unwinding")
}

func TestEndOfDeliveryClearRunsAfterFinalFlush(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	w.End()
	w.Flush()

	// The final flush is the syscall pushing the delivery's last bytes; it must still run under
	// the armed deadline, with the clear strictly after it. Clearing first would let a stalled
	// client block the handler in that flush with no bound.
	assert.Equal(t, []string{"arm", "write", "flush", "arm"}, base.eventLog(),
		"the clearing SetWriteDeadline must come after the final flush")
	armed := base.armed()
	assert.True(t, armed[len(armed)-1].IsZero())
}

func TestStaleWaitDoesNotReleaseNewerDelivery(t *testing.T) {
	base := newDeadlineConn()
	w := WrapGated(base, time.Minute)
	w.Begin()
	stale := w.Done() // delivery 1's channel
	w.Begin()         // delivery 2 begins; delivery 1's channel is closed here

	// Drive the cancellation branch with the stale capture repeatedly (which select arm runs is
	// random, since both are ready): the identity guard must keep a stale waiter from closing
	// delivery 2's done and releasing its slot early.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		w.waitAndFinish(ctx, stale)
	}
	assertOpen(t, w.Done(), "a stale waiter must not release the newer delivery")
}

func TestProducerHandlerSplitIsRaceFree(t *testing.T) {
	// The real shape: the producer goroutine drives Begin/End/WaitAndFinish while the handler
	// goroutine drives Write/Flush. Under -race this pins the lock discipline on the state
	// fields that Flush samples and Begin/End mutate (a single-goroutine lifecycle test cannot).
	w := WrapGated(&syncConn{}, 2*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // producer
		defer wg.Done()
		for range 300 {
			w.Begin()
			w.End()
			w.WaitAndFinish(ctx)
		}
	}()
	go func() { // handler
		defer wg.Done()
		for range 300 {
			_, _ = w.Write([]byte("data: x\n\n"))
			w.Flush()
		}
	}()
	wg.Wait()
}

func TestConcurrentLifecycleIsRaceFree(t *testing.T) {
	// Drives Done/WaitAndFinish concurrently against a Begin/End/Flush loop so the race detector
	// pins the lock discipline on the done channel. (The write-path split lives in
	// TestProducerHandlerSplitIsRaceFree, which runs Flush and Begin/End on different goroutines.)
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 200 {
			w.Begin()
			_, _ = w.Write([]byte("data: x\n\n"))
			w.End()
			w.Flush()
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			select {
			case <-w.Done():
			default:
			}
		}
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 200 {
			w.WaitAndFinish(ctx)
		}
	}()
	wg.Wait()
}

// zeroWriteConn is a contract-violating writer that always reports (0, nil) from both write
// paths; the chunk loops must not spin on it.
type zeroWriteConn struct{ *httptest.ResponseRecorder }

func (zeroWriteConn) Write([]byte) (int, error)        { return 0, nil }
func (zeroWriteConn) WriteString(string) (int, error)  { return 0, nil }
func (zeroWriteConn) SetWriteDeadline(time.Time) error { return nil }

func TestWriteZeroNilWriterReturnsShortWrite(t *testing.T) {
	w := Wrap(zeroWriteConn{httptest.NewRecorder()}, 2*time.Minute)
	var n int
	var err error
	// The timeout makes a removed guard fail the test rather than hang the binary.
	runWithTimeout(t, "Write on a (0, nil) writer must return", func() {
		n, err = w.Write(make([]byte, chunkSize))
	})
	assert.ErrorIs(t, err, io.ErrShortWrite)
	assert.Zero(t, n)
}

func TestWriteStringZeroNilWriterReturnsShortWrite(t *testing.T) {
	w := Wrap(zeroWriteConn{httptest.NewRecorder()}, 2*time.Minute)
	var n int
	var err error
	runWithTimeout(t, "WriteString on a (0, nil) writer must return", func() {
		n, err = w.WriteString(string(make([]byte, chunkSize)))
	})
	assert.ErrorIs(t, err, io.ErrShortWrite)
	assert.Zero(t, n)
}

// noFlushConn is a minimal ResponseWriter that records deadlines but cannot flush.
// panicFlushConn is a noFlushConn whose Flush panics, to prove the teardown still runs during
// unwinding.
type panicFlushConn struct{ *noFlushConn }

func (panicFlushConn) Flush() { panic("flush exploded") }

// syncConn is a minimal ResponseWriter whose every method is safe for concurrent use, so
// producer/handler-split race tests exercise the Writer's own locking rather than the fake's.
type syncConn struct {
	mu  sync.Mutex
	hdr http.Header
}

func (c *syncConn) Header() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}
func (c *syncConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *syncConn) WriteHeader(int)                  {}
func (c *syncConn) SetWriteDeadline(time.Time) error { return nil }
func (c *syncConn) Flush()                           {}

type noFlushConn struct {
	hdr       http.Header
	deadlines []time.Time
}

func (c *noFlushConn) Header() http.Header {
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}
func (c *noFlushConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *noFlushConn) WriteHeader(int)             {}
func (c *noFlushConn) SetWriteDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	return nil
}

func TestFlushTearsDownOnNonFlusherChain(t *testing.T) {
	base := &noFlushConn{}
	w := WrapGated(base, 2*time.Minute)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms
	require.NoError(t, err)
	w.End()

	// Even though the underlying writer cannot flush, the end-of-delivery teardown must still
	// clear the deadline and release the waiter, so a non-flushing chain cannot strand either.
	w.Flush()
	require.NotEmpty(t, base.deadlines)
	assert.True(t, base.deadlines[len(base.deadlines)-1].IsZero(), "teardown must clear the deadline without a Flusher")
	assertClosed(t, w.Done(), "teardown must release the waiter without a Flusher")
}

func TestCutMovesDeadlineToNowAndStaysCut(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms start+21s
	require.NoError(t, err)

	w.Cut()
	armed := base.armed()
	require.Len(t, armed, 2)
	assert.Equal(t, c.now(), armed[1], "Cut must move the deadline to now")

	// A later chunk of the same delivery must never get a fresh budget: every deadline after
	// the cut is "now", not a future instant. (The write itself passes through; the transport
	// fails it in production.)
	c.advance(time.Second)
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	armed = base.armed()
	assert.Equal(t, c.now(), armed[len(armed)-1], "a write after the cut must re-assert now, never a future budget")

	// Idempotent: a second Cut adds nothing.
	before := len(base.armed())
	w.Cut()
	assert.Len(t, base.armed(), before)
}

func TestCutIsTerminalForTheConnection(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)

	// A cut that lands before Begin -- the watcher firing in the window between a slot
	// acquisition and the delivery's start -- must not be lost, but it must also not touch
	// the deadline yet: a connection with no gated delivery still closes gracefully.
	w.Cut()
	require.Empty(t, base.armed(), "a cut with no delivery in progress must leave the deadline alone")

	// The delivery that then begins fails on its first write instead of getting a budget.
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, c.now(), armed[0], "a delivery begun after the cut must arm now, not a fresh budget")
}

func TestCutRacesWritesSafely(t *testing.T) {
	// The watcher calls Cut from its own goroutine while the handler is writing; the race
	// detector pins the lock discipline between them.
	w := WrapGated(&syncConn{}, 2*time.Minute)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			w.Begin()
			_, _ = w.Write([]byte("data: x\n\n"))
			w.End()
			w.Flush()
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			w.Cut()
		}
	}()
	wg.Wait()
}

func TestReBeginReleasesPriorWaiter(t *testing.T) {
	base := newDeadlineConn()
	w := WrapGated(base, time.Minute)
	w.Begin()
	first := w.Done()
	assertOpen(t, first, "first delivery's Done")

	w.Begin()
	assertClosed(t, first, "prior Done after re-Begin")
	assertOpen(t, w.Done(), "new delivery's Done")
}

func TestReBeginResetsEnding(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	w.End() // delivery 1 abandoned after End

	// Delivery 2 must not inherit delivery 1's ending flag: the first flush after re-Begin would
	// otherwise tear delivery 2 down before its basis is written.
	w.Begin()
	w.Flush()
	assertOpen(t, w.Done(), "a flush right after re-Begin must not tear down the new delivery")
}

func TestReBeginResetsCapAnchor(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 30*time.Second, true)

	w.Begin()
	start1 := c.now()
	_, err := w.Write(make([]byte, chunkSize)) // anchors delivery 1's cap at start1
	require.NoError(t, err)
	require.Equal(t, 21*time.Second, base.armed()[0].Sub(start1))
	w.End()
	w.Flush()

	// Delivery 2 starts 25s later; its cap must be anchored to its own start, not delivery 1's
	// (which would clamp it to start1+30s = 5s from now).
	c.advance(25 * time.Second)
	w.Begin()
	start2 := c.now()
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	armed := base.armed()
	assert.Equal(t, 21*time.Second, armed[len(armed)-1].Sub(start2), "delivery 2's budget must use its own cap anchor")
}

func TestReBeginResetsDeadlineRatchet(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)

	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // arms start+21s
	require.NoError(t, err)
	w.End()
	w.Flush()
	armedBefore := len(base.armed()) // includes the clearing zero

	// Delivery 2's small write computes a deadline (~slack) well below delivery 1's high-water
	// mark; a ratchet that survived re-Begin would suppress it, leaving the write deadline-less.
	w.Begin()
	start2 := c.now()
	_, err = w.Write(make([]byte, 4096))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, armedBefore+1, "delivery 2's first write must arm despite delivery 1's higher deadline")
	assert.Equal(t, slack+time.Duration(4096)*time.Second/time.Duration(minBytesPerSec), armed[len(armed)-1].Sub(start2))
}

func TestMaxHoldAnchoredToDeliveryStartNotPerWrite(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 30*time.Second, false)

	start := c.now()
	_, err := w.Write(make([]byte, chunkSize)) // arms start+21s
	require.NoError(t, err)

	// 25s into the delivery, another chunk's floor budget (now+21s = start+46s) exceeds the cap.
	// The cap must clamp to the DELIVERY's start (start+30s); a per-write anchor (now+30s) would
	// mean no absolute backstop at all.
	c.advance(25 * time.Second)
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 2)
	assert.Equal(t, 30*time.Second, armed[1].Sub(start), "the cap must be anchored to the delivery start")
}

func TestMinExtensionSkipsTrivialReArms(t *testing.T) {
	base := newDeadlineConn()
	base.advancePerWrite = 50 * time.Millisecond // half of minExtension per chunk
	w, c := wrapClocked(base, 5*time.Minute, false)

	// Chunk 1 arms start+21s. Chunk 2's deadline is only 50ms later -- below minExtension, so it
	// is skipped. Chunk 3's is 100ms later than the armed one -- at the threshold, so it arms.
	start := c.now()
	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 2, "sub-minExtension re-arms must be skipped")
	assert.Equal(t, 21*time.Second, armed[0].Sub(start))
	assert.Equal(t, 21*time.Second+100*time.Millisecond, armed[1].Sub(start))
}

func TestArmIsNoOpAfterDeliveryEnds(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	w.End()
	w.Flush()
	calls := base.calls()

	// arm's own active check covers the window between a Write's activity check and the arm; an
	// arm that slips through after teardown would put a stray deadline on a delivered stream.
	w.arm(4096)
	assert.Equal(t, calls, base.calls(), "arm after the delivery ended must not set a deadline")
}

func TestFlushDoesNotClearNewerDeliveryDeadline(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize)) // delivery 1 armed
	require.NoError(t, err)
	w.End()

	// A newer delivery begins during delivery 1's end-of-batch flush (modeling the producer
	// racing the handler's flush). The stale flush must not tear down the newer delivery.
	base.onFlush = func() {
		base.onFlush = nil // once
		w.Begin()
		c.advance(time.Second)
		_, _ = w.Write(make([]byte, chunkSize)) // delivery 2 armed
	}
	w.Flush()

	armed := base.armed()
	last := armed[len(armed)-1]
	assert.False(t, last.IsZero(), "a stale flush must not clear the newer delivery's deadline")
}

func TestFlushAfterCompletionDoesNotPanic(t *testing.T) {
	base := newDeadlineConn()
	w := WrapGated(base, 2*time.Minute)
	w.Begin()
	_, err := w.Write([]byte("event: put\n\n"))
	require.NoError(t, err)
	w.End()
	w.Flush() // clears and closes done

	// A second flush of an already-completed delivery (the doneClosed guard) must not double-close.
	assert.NotPanics(t, func() { w.Flush() })
	assertClosed(t, w.Done(), "Done stays closed after a redundant flush")
}

func TestPollWrapDeadlineDoesNotLingerOntoNextKeepAliveRequest(t *testing.T) {
	// A poll (Wrap) delivery arms a per-write deadline and relies on net/http to reset the
	// connection's write deadline when the handler returns -- this server sets no WriteTimeout.
	// Pin that reliance: net/http clears it unconditionally, so an armed deadline from one request
	// cannot linger onto the next request on a reused HTTP/1.1 keep-alive connection. (If a future
	// Go release changed this, Wrap would need to clear the deadline itself.)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := n.Add(1)
		if i == 1 {
			// A tiny maxHold makes the armed deadline expire well before request 2's write, so a
			// failure to clear would surface as a failed second request rather than passing by luck.
			pw := Wrap(w, 50*time.Millisecond)
			_, _ = pw.Write([]byte("resp-1"))
			return
		}
		_, _ = io.WriteString(w, "resp-2")
	}))
	defer srv.Close()
	require.Zero(t, srv.Config.WriteTimeout, "test assumes no server WriteTimeout, matching relay")

	client := srv.Client() // keep-alive transport, reuses the connection
	var reused atomic.Bool
	get := func() (string, error) {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			return "", err
		}
		trace := &httptrace.ClientTrace{GotConn: func(ci httptrace.GotConnInfo) { reused.Store(ci.Reused) }}
		resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	b, err := get()
	require.NoError(t, err)
	require.Equal(t, "resp-1", b)

	time.Sleep(150 * time.Millisecond) // past the 50ms deadline armed in request 1
	b, err = get()
	require.NoError(t, err, "armed deadline lingered onto the reused connection")
	assert.Equal(t, "resp-2", b)
	// The reuse assertion is what gives this test teeth: if the deadline lingered and killed
	// the pooled connection, the transport would silently retry the idempotent GET on a fresh
	// connection and the body assertions above would still pass.
	assert.True(t, reused.Load(), "request 2 must run on the SAME keep-alive connection")
}

func TestUnwrapReachesBase(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, time.Minute)
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	assert.NoError(t, err)
	assert.NotEmpty(t, base.armed(), "SetWriteDeadline did not reach base through initwrite.Writer.Unwrap")
}

func TestEmptyWriteNoDeadline(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, time.Minute)
	_, err := w.Write(nil)
	require.NoError(t, err)
	assert.Empty(t, base.armed(), "an empty write should not arm a deadline")
}

func runWithTimeout(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: did not return within the timeout", what)
	}
}

func assertClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s: expected a closed channel, but it blocked", what)
	}
}

func assertOpen(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s: expected an open channel, but it was closed", what)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWaitAndFinishReportsTheOutcome(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	w.End()
	go w.Flush()
	assert.True(t, w.WaitAndFinish(context.Background()), "a flushed delivery must report true")

	w2, _ := wrapClocked(newDeadlineConn(), 2*time.Minute, true)
	w2.Begin()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, w2.WaitAndFinish(gone), "an ended connection must report false")
}

func TestCapEngagedReportsTheClamp(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 500*time.Millisecond, true) // cap below one chunk's budget
	w.Begin()
	assert.False(t, w.CapEngaged())
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	assert.True(t, w.CapEngaged(), "the clamped deadline must set the flag")

	// A new delivery starts clean.
	w.End()
	w.Flush()
	w.Begin()
	assert.False(t, w.CapEngaged(), "Begin must clear the flag")
}

func TestDeadlineSetErrorsAreCounted(t *testing.T) {
	base := newDeadlineConn()
	base.setErr = errors.New("deadline not supported")
	w, _ := wrapClocked(base, 2*time.Minute, false)
	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Equal(t, int64(3), w.DeadlineSetErrors(), "each failed SetWriteDeadline must count")
}
