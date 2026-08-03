package initwrite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
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
	// holds the Writer and calls WaitAndFinish after the handler has returned and its context is
	// cancelled. Touching the connection here nil-panics inside net/http and crashes the process;
	// WaitAndFinish must not. (A panic in that goroutine would take the whole test binary down.)
	finished := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w := WrapGated(rw, 2*time.Minute)
		w.Begin()
		_, _ = w.Write([]byte("data: hi\n\n"))
		w.Flush()
		go func() {
			defer close(finished)
			w.WaitAndFinish(r.Context()) // fires on ctx.Done after the handler returns
		}()
		// handler returns here, completing the stream and cancelling r.Context()
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
