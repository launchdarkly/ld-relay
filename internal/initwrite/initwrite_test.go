package initwrite

import (
	"errors"
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

// deadlineConn is a ResponseWriter that records every write deadline armed on it, so tests can
// assert the progress-aware arming logic without a real socket. It forwards Flush to the
// embedded recorder (so Flushed stays observable) and guards its recorded state with a mutex.
// If a clock is attached it advances by advancePerWrite on each Write, so a multi-chunk write
// can exercise per-chunk re-arming deterministically.
type deadlineConn struct {
	*httptest.ResponseRecorder

	mu              sync.Mutex
	deadlines       []time.Time
	setErr          error // returned from SetWriteDeadline when non-nil, to simulate a failing syscall
	setCalls        int
	flushes         int
	clock           *fakeClock
	advancePerWrite time.Duration
}

func newDeadlineConn() *deadlineConn { return &deadlineConn{ResponseRecorder: httptest.NewRecorder()} }

func (d *deadlineConn) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setCalls++
	if d.setErr != nil {
		return d.setErr
	}
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineConn) Write(p []byte) (int, error) {
	if d.clock != nil && d.advancePerWrite > 0 {
		d.clock.advance(d.advancePerWrite)
	}
	return d.ResponseRecorder.Write(p)
}

// WriteString mirrors Write so the string and byte paths advance the fake clock identically;
// without it, io.WriteString would reach the embedded recorder's own WriteString and skip the
// clock advance, making the two paths incomparable.
func (d *deadlineConn) WriteString(s string) (int, error) {
	if d.clock != nil && d.advancePerWrite > 0 {
		d.clock.advance(d.advancePerWrite)
	}
	return d.ResponseRecorder.WriteString(s)
}

func (d *deadlineConn) Flush() {
	d.mu.Lock()
	d.flushes++
	d.mu.Unlock()
	d.ResponseRecorder.Flush()
}

func (d *deadlineConn) armed() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.deadlines)
}

func (d *deadlineConn) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setCalls
}

// wrapClocked builds a Writer driven by a deterministic clock shared with base.
func wrapClocked(base *deadlineConn, maxHold time.Duration, gated bool) (*Writer, *fakeClock) {
	c := newFakeClock()
	base.clock = c
	w := Wrap(base, maxHold)
	if gated {
		w = WrapGated(base, maxHold)
	}
	w.now = c.now
	return w, c
}

// perChunkBudget is the deadline a full 1 MiB chunk should arm: time to send it at the floor
// plus slack. Tests assert against this exact value so a weaker/stronger floor or slack fails.
var perChunkBudget = time.Duration(chunkSize)*time.Second/time.Duration(minBytesPerSec) + slack

func TestWriteArmsFullChunkAtFloorPlusSlack(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, false)

	start := c.now()
	n, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	assert.Equal(t, chunkSize, n)

	armed := base.armed()
	require.Len(t, armed, 1, "a single full chunk should arm exactly one deadline")
	assert.Equal(t, perChunkBudget, armed[0].Sub(start)) // 16s at 64 KiB/s + 5s slack = 21s
}

func TestWriteSmallPayloadArmsSlackOnly(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, false)

	start := c.now()
	_, err := w.Write(make([]byte, 4096))
	require.NoError(t, err)

	armed := base.armed()
	require.Len(t, armed, 1)
	// A tiny payload's time-at-floor rounds to ~0, so the deadline is essentially just slack.
	assert.Equal(t, slack+time.Duration(4096)*time.Second/time.Duration(minBytesPerSec), armed[0].Sub(start))
}

func TestWriteReArmsEachChunk(t *testing.T) {
	base := newDeadlineConn()
	base.advancePerWrite = minExtension // enough progress between chunks to re-arm
	w, _ := wrapClocked(base, 5*time.Minute, false)

	// A multi-chunk write must arm once per full chunk, not once for the whole payload: that is
	// what keeps a client that stalls partway through from holding a slot past the floor.
	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Len(t, base.armed(), 3, "expected one arm per 1 MiB chunk")
}

func TestWriteCapsDeadlineAtMaxHold(t *testing.T) {
	base := newDeadlineConn()
	// A tiny cap (well under the per-chunk budget) must clamp the armed deadline to the cap.
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
	// A non-positive maxHold leaves only the per-write floor; the deadline is not clamped.
	w, c := wrapClocked(base, 0, false)
	start := c.now()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)

	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, perChunkBudget, armed[0].Sub(start))
}

func TestArmDoesNotAdvanceOnSetDeadlineFailure(t *testing.T) {
	base := newDeadlineConn()
	base.setErr = errors.New("deadline not supported")
	base.advancePerWrite = minExtension
	w, _ := wrapClocked(base, 2*time.Minute, false)

	// If SetWriteDeadline keeps failing, the writer must keep attempting it on later chunks
	// rather than recording a deadline it never set and going silently inert.
	_, err := w.Write(make([]byte, 3*chunkSize))
	require.NoError(t, err)
	assert.Equal(t, 3, base.calls(), "each chunk should retry SetWriteDeadline after a failure")
}

func TestGatedArmsOnlyBetweenBeginAndEndFlush(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapClocked(base, 2*time.Minute, true)

	// Before Begin: writes pass through unarmed (the stream's headers/initial bytes carry no
	// deadline until a delivery starts).
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
	assert.Equal(t, perChunkBudget, armed[0].Sub(start))

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

func TestFinishClearsDeadlineAndClosesDone(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapClocked(base, 2*time.Minute, true)
	w.Begin()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	done := w.Done()

	// Finish is the backstop for a delivery that ends without a final flush: it clears the
	// deadline and releases the waiter.
	w.Finish()
	armed := base.armed()
	require.NotEmpty(t, armed)
	assert.True(t, armed[len(armed)-1].IsZero(), "Finish should clear the write deadline")
	assertClosed(t, done, "Done after Finish")

	// Idempotent: a second Finish neither panics nor re-clears.
	before := len(base.armed())
	w.Finish()
	assert.Len(t, base.armed(), before, "a second Finish should be a no-op")

	// Inert afterwards: writes pass through unarmed.
	_, err = w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	assert.Len(t, base.armed(), before, "no deadline should arm after Finish")
}

func TestReBeginReleasesPriorWaiter(t *testing.T) {
	base := newDeadlineConn()
	w := WrapGated(base, time.Minute)
	w.Begin()
	first := w.Done()
	assertOpen(t, first, "first delivery's Done")

	// A second Begin (a defensive case) must release the prior delivery's waiter rather than
	// orphan it, and hand out a fresh open channel.
	w.Begin()
	assertClosed(t, first, "prior Done after re-Begin")
	assertOpen(t, w.Done(), "new delivery's Done")
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

func TestUnwrapReachesBase(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, time.Minute)
	// The deadline set through the controller must reach the base connection.
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
