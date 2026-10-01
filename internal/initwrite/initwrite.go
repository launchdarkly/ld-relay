// Package initwrite provides a progress-aware write deadline for SDK responses -- SSE streams
// and poll responses. It bounds how long a slow or stalled client can hold a connection, and a
// budget slot if it has one, without false-killing a slow-but-steady client: a
// minimum-throughput floor drives the deadline, and an absolute cap bounds a single unit of
// output.
//
// A Writer must be constructed with Wrap, WrapResponse, WrapGated, or WrapStream; the zero
// value is not usable.
//
// Accounting. A deadline belongs to a unit of output: a poll response, a gated delivery, or
// one inferred stream unit (see below). It is start + written/floor + slack, capped at
// start + cap, where start is the unit's first write and written counts every byte the unit
// has passed to the connection, including the chunk about to be written. The count is
// cumulative, so a client that reads below the floor in small steps falls behind the deadline;
// a per-write budget would let it re-arm a fresh slack on every write. Large writes are sliced
// into chunks and the deadline is armed before each chunk, so a client that stalls at the
// start of a large write is cut after one chunk's budget, not the whole write's.
//
// Connection ownership. Only the goroutine running the HTTP handler may set or clear the write
// deadline, and it does so through this Writer: arming it before each write and clearing it at
// the end of a unit. A write deadline is a capability scoped to the handler's lifetime -- after
// the handler returns, the underlying connection may be recycled or, on HTTP/2, gone entirely,
// so touching it then is unsafe. For a gated stream the producer goroutine that feeds events
// can outlive the handler (the SSE server drains it once the client leaves), so it must never
// touch the connection; it coordinates only through Begin/End/Done/WaitAndFinish. That keeps
// every deadline call within the handler's lifetime.
//
// Three shapes are supported:
//
//   - Poll (Wrap, WrapResponse): a request/response delivery. The whole response is one unit,
//     and the deadline is armed on every write for the lifetime of the wrapper. net/http resets
//     the connection's write deadline when the handler returns, so it does not linger onto a
//     later request on a kept-alive connection.
//   - Gated stream (WrapGated): a persistent SSE connection, where the connection's write
//     deadline is not reset between the initial delivery and later delta or heartbeat traffic.
//     The deadline is armed only between Begin and the end-of-delivery flush, and is cleared
//     there, so live traffic and idle periods carry no deadline. This matters on HTTP/2, where
//     a lingering deadline is a self-firing timer that would reset an otherwise idle stream.
//   - Stream (WrapStream): a persistent SSE connection where every write carries a deadline.
//     The Writer infers units from the calls the SSE server makes: a unit starts at the first
//     Write or Flush after an idle period, and the next Flush ends it and clears the deadline.
//     The eventsource server flushes after the response headers, after each live event or
//     comment, and once at the end of each replay batch, so each of those is one unit and an
//     idle stream carries no deadline. A Stream writer can also carry gated deliveries: Begin
//     starts a delivery that spans flushes until the flush after End, with the delivery's own
//     limits.
//
// Begin, End, Done and WaitAndFinish apply only to the gated and stream shapes.
//
// Ending a gated delivery. The producer must call End on EVERY exit after Begin -- whether it
// wrote the whole basis, or is abandoning the delivery after an error -- and before it closes
// the batch channel. End marks the delivery finished; the handler's end-of-delivery flush then
// clears the deadline and releases the waiter. There are three ways a delivery ends, and End is
// what makes the first two safe:
//
//   - Clean finish: the producer wrote everything, calls End, and the flush clears the deadline,
//     leaving the now-idle stream alone.
//   - Producer error with the client still healthy: the producer calls End on the error path too
//     -- even an exit that wrote nothing, since the delivery stays active and a later heartbeat
//     write would arm a deadline that then fires with nothing left to finish it. Skipping End is
//     a bug: the deadline is never cleared, and the healthy stream is killed anyway -- on
//     HTTP/1.1 at the first write after the absolute cap expires, and on HTTP/2 within a few
//     seconds (the write slack) of the last write, because a deadline there is a self-firing
//     timer. (A heartbeat interval shorter than the slack keeps postponing the HTTP/2 fire, so
//     the bug can be invisible under fast test heartbeats yet fatal at a production interval.)
//     Disabling the cap does not make the omission safe: HTTP/2 still dies on the slack timer,
//     while on HTTP/1.1 nothing fires at all and the budget slot is pinned permanently instead.
//   - Client goes away first: WaitAndFinish returns on context cancellation without touching the
//     connection; the per-write deadline already armed bounds anything still in flight, and the
//     connection teardown clears it.
//
// The producer waits via WaitAndFinish, which frees the budget slot once the delivery has been
// flushed or the client has gone.
//
// A client sustaining at least the throughput floor keeps up with the deadline and is not cut
// for being slow; one that stalls, or whose average rate over the unit drops below the floor,
// has its write fail, which the HTTP server turns into a closed connection. The absolute cap
// backstops a client that stays right at the floor on a very large unit: once the unit would
// exceed the cap at the floor, the cap governs and the client is cut. A cap of zero or less
// disables it, leaving only the floor; callers that want the backstop must pass a positive
// value.
package initwrite

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// WriteSlack is the fixed allowance added to each initialization-delivery deadline. It is
// exported so a caller that configures an absolute delivery cap can keep that cap at or above
// it; a cap below the slack would expire before even a small first write.
const WriteSlack = 5 * time.Second

const (
	// minBytesPerSec is the throughput floor of an initialization delivery.
	minBytesPerSec = 64 * 1024
	// chunkSize bounds how much is written under a single deadline. Large writes are sliced
	// so the deadline is re-armed mid-write; the string path copies at most one chunk at a
	// time rather than the whole payload at once.
	chunkSize = 1 << 20 // 1 MiB
	// slack is added to each initialization-delivery deadline to tolerate a brief stall.
	slack = WriteSlack
	// minExtension avoids a SetWriteDeadline syscall for a trivially small change.
	minExtension = 100 * time.Millisecond
)

// Limits sets the deadline of one unit of output: start + written/MinBytesPerSecond + Slack,
// capped at start + MaxHold. A MaxHold of zero or less disables the cap.
type Limits struct {
	MinBytesPerSecond int
	Slack             time.Duration
	MaxHold           time.Duration
}

// DeliveryLimits returns the limits of an initialization delivery: a 64 KiB/s floor and
// WriteSlack, capped at maxHold. If unit is not nil and has a higher floor, the delivery uses
// that floor instead, so an initialization delivery is never held to a lower floor than the
// rest of the connection's writes.
func DeliveryLimits(maxHold time.Duration, unit *Limits) Limits {
	l := Limits{MinBytesPerSecond: minBytesPerSec, Slack: slack, MaxHold: maxHold}
	if unit != nil && unit.MinBytesPerSecond > l.MinBytesPerSecond {
		l.MinBytesPerSecond = unit.MinBytesPerSecond
	}
	return l
}

// budget returns how long a unit that has written n bytes may take under l.
func (l Limits) budget(n int) time.Duration {
	// The whole seconds and the remainder are converted separately, so a large count cannot
	// overflow time.Duration.
	rate := time.Duration(l.MinBytesPerSecond)
	b := time.Duration(n)
	return (b/rate)*time.Second + (b%rate)*time.Second/rate + l.Slack
}

// Writer wraps an http.ResponseWriter and arms a progress-aware write deadline on the
// underlying connection while a unit of output is active. Construct it with Wrap, WrapResponse,
// WrapGated, or WrapStream.
type Writer struct {
	http.ResponseWriter
	rc *http.ResponseController
	// delivery is the limits of a poll response or a gated delivery.
	delivery Limits
	// unit is the limits of an inferred stream unit. It is nil unless the Writer has the
	// stream shape.
	unit *Limits
	now  func() time.Time // time source; overridable in tests

	mu     sync.Mutex
	active bool
	// inDelivery records that the active unit is a gated delivery, which only the flush
	// after End ends. When it is false and active is true, the active unit is an inferred
	// stream unit (or, for the poll shape, the response).
	inDelivery   bool
	ending       bool
	limits       Limits
	msgStart     time.Time
	written      int
	lastDeadline time.Time
	// gen identifies the current gated delivery. Begin bumps it so a teardown (the end-of-batch
	// flush) that observed an earlier delivery cannot clear the deadline of a newer one.
	gen uint64
	// capEngaged records that the absolute cap clamped a deadline for the current delivery:
	// the delivery is large enough that the cap, not the floor, decides when it is cut.
	capEngaged bool
	// deadlineSetErrors counts the SetWriteDeadline calls that failed. When it grows, the
	// deadline protection is not in force on this connection.
	deadlineSetErrors atomic.Int64
	// cut records that the connection was cut because its client is gone. The flag is
	// terminal -- Begin does not clear it -- so a cut that lands between a slot acquisition
	// and Begin is not lost: the delivery that then begins fails on its first write instead
	// of draining. arm must never grant a cut delivery a fresh budget.
	cut bool
	// done is closed once the current gated delivery ends. It is never nil: outside a delivery
	// it is a closed channel, so a producer waiting on it releases its slot immediately rather
	// than blocking forever. doneClosed guards against a double close.
	done       chan struct{}
	doneClosed bool
}

// Wrap returns a Writer for a poll (request/response) delivery with the initialization-delivery
// limits (see DeliveryLimits): the deadline is armed on every write. net/http resets the
// connection deadline when the handler returns.
func Wrap(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return WrapResponse(w, DeliveryLimits(maxHold, nil))
}

// WrapResponse returns a Writer for a poll (request/response) delivery with the given limits:
// the whole response is one unit, and the deadline is armed on every write.
func WrapResponse(w http.ResponseWriter, limits Limits) *Writer {
	iw := newWriter(w, limits, nil)
	iw.active = true
	iw.limits = limits
	return iw
}

// WrapGated returns a Writer for a persistent stream with initialization-delivery limits: it
// arms nothing until Begin, and clears the deadline at the End-triggered end-of-delivery flush.
func WrapGated(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return newWriter(w, DeliveryLimits(maxHold, nil), nil)
}

// WrapStream returns a Writer for a persistent stream that arms a deadline for every inferred
// unit of output, with the given unit limits. Gated deliveries started with Begin use the
// delivery limits instead.
func WrapStream(w http.ResponseWriter, unit Limits, delivery Limits) *Writer {
	return newWriter(w, delivery, &unit)
}

func newWriter(w http.ResponseWriter, delivery Limits, unit *Limits) *Writer {
	// done starts closed so that, with no delivery in progress, a producer that waits on Done
	// is released at once instead of pinning its slot.
	d := make(chan struct{})
	close(d)
	return &Writer{
		ResponseWriter: w,
		rc:             http.NewResponseController(w),
		delivery:       delivery,
		unit:           unit,
		now:            time.Now,
		done:           d,
		doneClosed:     true,
	}
}

// Begin marks the start of a gated delivery; writes from here on arm the delivery's deadline. It
// is called from the producer before it starts producing events. If a previous delivery never
// completed, its Done channel is closed here so any waiter is released rather than orphaned. An
// inferred stream unit that is in progress becomes part of the delivery.
func (w *Writer) Begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = true
	w.inDelivery = true
	w.ending = false
	w.capEngaged = false
	w.startUnitLocked(w.delivery)
	w.gen++
	if !w.doneClosed {
		close(w.done)
	}
	w.done = make(chan struct{})
	w.doneClosed = false
}

// Done returns a channel closed once the current gated delivery has ended. The channel is never
// nil: with no delivery in progress it is already closed, so a producer that waits on it is not
// left blocked. Call it only after Begin -- capturing it earlier returns the closed idle channel
// and would release the slot while the delivery is still in flight. Prefer WaitAndFinish, which
// reads it at the right time.
func (w *Writer) Done() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.done
}

// End marks the delivery finished so the next flush tears it down. Call it on every exit after
// Begin -- a completed delivery AND an abandoned one (a serialization or store error), even one
// that wrote nothing -- and before closing the batch channel, so the handler's single end-of-batch
// flush observes it and clears the deadline. It is idempotent. When using defers, register the
// batch-channel close before this one (defer close(out), then defer w.End()), so End runs first.
// Omitting it on an error path leaves the armed deadline in place, which eventually cuts even a
// healthy client; releasing the slot without it is not enough, because only the handler goroutine
// can clear the connection's deadline.
func (w *Writer) End() {
	w.mu.Lock()
	w.ending = true
	w.mu.Unlock()
}

// WaitAndFinish holds until the current gated delivery's last byte has been flushed to the
// client (Done closes, the handler having cleared the deadline) or ctx is done (the client went
// away). The caller releases its budget slot once this returns. It never touches the connection
// -- on cancellation the already-armed per-write deadline bounds anything still in flight and the
// connection teardown clears it -- so it is safe on the producer goroutine even after the handler
// has returned. Call it after Begin. It must be given the request's context, or another that is
// cancelled when the client disconnects: a never-cancelled context would block the producer, and
// its slot, until the delivery flushes, and a context cancelled while the handler is still live
// (a shutdown or producer-error signal, say) would return early without ending the delivery,
// leaving the missed-End failure described in the package comment -- use End for that.
//
// The return value reports the outcome: true when the delivery's last byte was flushed to
// the client, false when the connection ended first. A false return does not say why the
// connection ended -- a client disconnect and a relay deadline cut look the same here.
func (w *Writer) WaitAndFinish(ctx context.Context) bool {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	return w.waitAndFinish(ctx, done)
}

// waitAndFinish is WaitAndFinish after the channel capture, split out so a test can drive the
// cancellation branch with a stale captured channel.
func (w *Writer) waitAndFinish(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		// Delivered: the handler's end-of-delivery flush closed done and cleared the deadline.
		return true
	case <-ctx.Done():
		// The client went away before the delivery finished. Release the waiter, but do not
		// touch the connection: it belongs to the handler, which may already have returned.
		w.mu.Lock()
		if w.done == done && !w.doneClosed { // still this delivery, and not already closed
			close(w.done)
			w.doneClosed = true
		}
		w.mu.Unlock()
		return false
	}
}

// Cut marks the connection as cut: the unit in progress -- or the next one to begin -- has its
// writes fail at once instead of draining until a deadline, because the client is gone. The mark
// is terminal; there is no way back. With a unit in progress the write deadline moves to now
// immediately; otherwise the deadline is left alone (so a connection that is idle still closes
// gracefully) and the first write of a later unit applies it instead.
//
// A write deadline is a capability scoped to the handler's lifetime (see the package
// comment), so Cut must be called only from the handler goroutine, or from a goroutine that
// provably cannot outlive the handler. The producer goroutine must never call it.
func (w *Writer) Cut() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cut {
		return
	}
	w.cut = true
	if w.active {
		_ = w.rc.SetWriteDeadline(w.now())
	}
}

// Exit bounds the final flush that net/http does after an HTTP/1 handler returns: it sets a
// deadline of now plus the slack, or now if the connection was cut. net/http clears the deadline
// after that flush. Call it only for HTTP/1, and only from the handler goroutine as the handler
// returns: on HTTP/2 a deadline left on a stream the client already closed can later send the
// client a stray stream reset.
func (w *Writer) Exit() {
	w.mu.Lock()
	defer w.mu.Unlock()
	dl := w.now()
	if !w.cut {
		l := w.delivery
		if w.unit != nil {
			l = *w.unit
		}
		dl = dl.Add(l.Slack)
	}
	if err := w.rc.SetWriteDeadline(dl); err != nil {
		w.deadlineSetErrors.Add(1)
	}
}

// CapEngaged reports whether the absolute cap clamped a deadline for the current delivery.
// When it is true, the delivery is large enough that the cap, not the throughput floor,
// decides when a slow client is cut.
func (w *Writer) CapEngaged() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.capEngaged
}

// DeadlineSetErrors returns how many SetWriteDeadline calls have failed on this connection.
// A value above zero means the deadline protection is not in force.
func (w *Writer) DeadlineSetErrors() int64 {
	return w.deadlineSetErrors.Load()
}

// Unwrap exposes the wrapped ResponseWriter so http.NewResponseController and other wrappers
// can reach the underlying connection through this one.
func (w *Writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Write slices p into chunks and arms the deadline before each.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 || !w.beginWrite() {
		return w.ResponseWriter.Write(p)
	}
	total := 0
	for total < len(p) {
		end := min(total+chunkSize, len(p))
		w.arm(end - total)
		n, err := w.ResponseWriter.Write(p[total:end])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite // a (0, nil) writer would otherwise spin forever
		}
	}
	return total, nil
}

// WriteString satisfies io.StringWriter so that when the wrapped writer also implements it,
// eventsource's io.WriteString does not allocate a []byte copy of the whole payload; it slices
// the string and, at worst, copies at most one chunk at a time.
func (w *Writer) WriteString(s string) (int, error) {
	if len(s) == 0 || !w.beginWrite() {
		return io.WriteString(w.ResponseWriter, s)
	}
	total := 0
	for total < len(s) {
		end := min(total+chunkSize, len(s))
		w.arm(end - total)
		n, err := io.WriteString(w.ResponseWriter, s[total:end])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

// Flush flushes buffered bytes and ends the unit it completes:
//
//   - At the end-of-delivery flush (after End), it clears the deadline and releases the
//     waiter. The state is sampled BEFORE flushing, so a delivery that begins during the
//     flush is untouched, and a stale flush cannot wipe a newer delivery's deadline.
//   - Inside a gated delivery that has not ended, it only flushes.
//   - For the stream shape outside a delivery, it ends the inferred unit and clears the
//     deadline. A Flush with no unit in progress (the response headers, say) is a unit of its
//     own: it arms a deadline for zero bytes first.
//
// Flush runs on the handler goroutine, within the connection's lifetime. The teardown is
// deferred, so it clears the deadline even if the flush itself is unsupported, fails, or panics.
func (w *Writer) Flush() {
	w.mu.Lock()
	if w.unit != nil && !w.active {
		w.active = true
		w.startUnitLocked(*w.unit)
		w.armLocked(0)
	}
	active, inDelivery, ending, gen := w.active, w.inDelivery, w.ending, w.gen
	w.mu.Unlock()

	switch {
	case inDelivery && ending:
		defer w.teardown(gen)
	case active && !inDelivery && w.unit != nil:
		defer w.endUnit(gen)
	}
	_ = w.rc.Flush() // best-effort; dispatches to the first Flusher in the Unwrap chain
}

// teardown ends the delivery identified by gen: it clears the deadline and releases the waiter.
func (w *Writer) teardown(gen uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gen != gen || !w.inDelivery {
		return // a newer delivery began, or this one was already ended
	}
	w.active = false
	w.inDelivery = false
	// The close is deferred so the waiter is released even if the deadline clear panics. The
	// doneClosed guard keeps this from double-closing when WaitAndFinish's ctx branch closed
	// done first (the client left as the delivery completed).
	defer func() {
		if !w.doneClosed {
			close(w.done)
			w.doneClosed = true
		}
	}()
	_ = w.rc.SetWriteDeadline(time.Time{})
}

// endUnit ends an inferred stream unit and clears its deadline, unless a gated delivery began
// since the Flush that ended it sampled the state.
func (w *Writer) endUnit(gen uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gen != gen || w.inDelivery || !w.active {
		return
	}
	w.active = false
	_ = w.rc.SetWriteDeadline(time.Time{})
}

// beginWrite reports whether the coming write must be armed, starting an inferred stream unit
// if the stream shape is idle.
func (w *Writer) beginWrite() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.active && w.unit != nil {
		w.active = true
		w.startUnitLocked(*w.unit)
	}
	return w.active
}

// startUnitLocked resets the accounting for a new unit with the given limits. The unit's start
// is its first arm.
func (w *Writer) startUnitLocked(l Limits) {
	w.limits = l
	w.msgStart = time.Time{}
	w.written = 0
	w.lastDeadline = time.Time{}
}

func (w *Writer) arm(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armLocked(n)
}

// armLocked counts n more bytes for the active unit and sets the deadline for everything the
// unit has written, capped by the unit's absolute deadline (msgStart + MaxHold). It never
// shortens the deadline within a unit and skips trivial changes. The deadline is recorded as
// current only when the underlying SetWriteDeadline succeeds, so a transient failure does not
// leave the writer believing it has armed a deadline it has not.
func (w *Writer) armLocked(n int) {
	if !w.active {
		return
	}
	if w.cut {
		// The connection is cut: this unit must fail now, not get a fresh budget. This runs
		// on the handler goroutine, so it may touch the deadline.
		_ = w.rc.SetWriteDeadline(w.now())
		return
	}
	now := w.now()
	if w.msgStart.IsZero() {
		w.msgStart = now
	}
	w.written += n
	dl := w.msgStart.Add(w.limits.budget(w.written))
	if w.limits.MaxHold > 0 {
		if capDL := w.msgStart.Add(w.limits.MaxHold); dl.After(capDL) {
			dl = capDL
			if w.inDelivery || w.unit == nil {
				w.capEngaged = true
			}
		}
	}
	if dl.After(w.lastDeadline) && dl.Sub(w.lastDeadline) >= minExtension {
		if err := w.rc.SetWriteDeadline(dl); err == nil {
			w.lastDeadline = dl
		} else {
			w.deadlineSetErrors.Add(1)
		}
	}
}

var (
	_ http.ResponseWriter = (*Writer)(nil)
	_ http.Flusher        = (*Writer)(nil)
	_ io.StringWriter     = (*Writer)(nil)
)
