// Package initwrite provides a progress-aware write deadline for initialization-delivery
// responses -- SSE stream replays and poll responses. It bounds how long a slow or stalled
// client can hold a budget slot without false-killing a slow-but-steady client: a
// minimum-throughput floor drives a per-write deadline, and an absolute cap bounds a single
// delivery.
//
// A Writer must be constructed with Wrap or WrapGated; the zero value is not usable.
//
// Connection ownership. Only the goroutine running the HTTP handler may set or clear the write
// deadline, and it does so through this Writer: arming it before each write and clearing it at
// the end-of-delivery flush. A write deadline is a capability scoped to the handler's lifetime
// -- after the handler returns, the underlying connection may be recycled or, on HTTP/2, gone
// entirely, so touching it then is unsafe. For a gated stream the producer goroutine that feeds
// events can outlive the handler (the SSE server drains it once the client leaves), so it must
// never touch the connection; it coordinates only through Begin/End/Done/WaitAndFinish. That
// keeps every deadline call within the handler's lifetime.
//
// Two shapes are supported:
//
//   - Poll (Wrap): a request/response delivery. The deadline is armed on every write for the
//     lifetime of the wrapper. net/http resets the connection's write deadline when the handler
//     returns, so it does not linger onto a later request on a kept-alive connection.
//   - Stream (WrapGated): a persistent SSE connection, where the connection's write deadline is
//     not reset between the initial delivery and later delta or heartbeat traffic. The deadline
//     is armed only between Begin and the end-of-delivery flush, and is cleared there, so live
//     traffic and idle periods carry no deadline. This matters on HTTP/2, where a lingering
//     deadline is a self-firing timer that would reset an otherwise idle stream. Begin, End,
//     Done and WaitAndFinish apply only to this shape.
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
// A client sustaining at least the throughput floor per write keeps re-arming and is not cut
// for being slow; one that stalls or drops below the floor on a write has its write fail, which
// the HTTP server turns into a closed connection. The absolute cap (maxHold, supplied by the
// caller) backstops a client that stays right at the floor on a very large payload: once a
// delivery would exceed maxHold at the floor, the cap governs and the client is cut. A maxHold
// of zero or less disables the cap, leaving only the per-write floor; callers that want the
// backstop must pass a positive value.
package initwrite

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// minBytesPerSec is the throughput floor a full-size chunk must sustain.
	minBytesPerSec = 64 * 1024
	// chunkSize bounds how much is written under a single deadline. Large writes are sliced
	// only to re-arm the deadline mid-write; the string path copies at most one chunk at a
	// time rather than the whole payload at once.
	chunkSize = 1 << 20 // 1 MiB
	// slack is added to each per-write deadline to tolerate a brief stall.
	slack = 5 * time.Second
	// minExtension avoids a SetWriteDeadline syscall for a trivially small change.
	minExtension = 100 * time.Millisecond
)

// Writer wraps an http.ResponseWriter and arms a progress-aware write deadline on the
// underlying connection while a delivery is active. Construct it with Wrap or WrapGated.
type Writer struct {
	http.ResponseWriter
	rc      *http.ResponseController
	maxHold time.Duration
	now     func() time.Time // time source; overridable in tests

	mu           sync.Mutex
	active       bool
	ending       bool
	msgStart     time.Time
	lastDeadline time.Time
	// gen identifies the current gated delivery. Begin bumps it so a teardown (the end-of-batch
	// flush) that observed an earlier delivery cannot clear the deadline of a newer one.
	gen uint64
	// cut records that the current delivery was cut: its deadline was moved to now so a
	// write blocked on a departed client fails at once. arm must not extend a cut deadline,
	// or a later chunk of the same delivery would resurrect the drain.
	cut bool
	// done is closed once the current gated delivery ends. It is never nil: outside a delivery
	// it is a closed channel, so a producer waiting on it releases its slot immediately rather
	// than blocking forever. doneClosed guards against a double close.
	done       chan struct{}
	doneClosed bool
}

// Wrap returns a Writer for a poll (request/response) delivery: the deadline is armed on every
// write. net/http resets the connection deadline when the handler returns.
func Wrap(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return newWriter(w, maxHold, true)
}

// WrapGated returns a Writer for a persistent stream: it arms nothing until Begin, and clears
// the deadline at the End-triggered end-of-delivery flush.
func WrapGated(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return newWriter(w, maxHold, false)
}

func newWriter(w http.ResponseWriter, maxHold time.Duration, active bool) *Writer {
	// done starts closed so that, with no delivery in progress, a producer that waits on Done
	// is released at once instead of pinning its slot.
	d := make(chan struct{})
	close(d)
	return &Writer{
		ResponseWriter: w,
		rc:             http.NewResponseController(w),
		maxHold:        maxHold,
		now:            time.Now,
		active:         active,
		done:           d,
		doneClosed:     true,
	}
}

// Begin marks the start of a gated delivery; writes from here on arm the deadline. It is
// called from the producer before it starts producing events. If a previous delivery never
// completed, its Done channel is closed here so any waiter is released rather than orphaned.
func (w *Writer) Begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = true
	w.ending = false
	w.cut = false
	w.msgStart = time.Time{}
	w.lastDeadline = time.Time{}
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
func (w *Writer) WaitAndFinish(ctx context.Context) {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	w.waitAndFinish(ctx, done)
}

// waitAndFinish is WaitAndFinish after the channel capture, split out so a test can drive the
// cancellation branch with a stale captured channel.
func (w *Writer) waitAndFinish(ctx context.Context, done <-chan struct{}) {
	select {
	case <-done:
		// Delivered: the handler's end-of-delivery flush closed done and cleared the deadline.
	case <-ctx.Done():
		// The client went away before the delivery finished. Release the waiter, but do not
		// touch the connection: it belongs to the handler, which may already have returned.
		w.mu.Lock()
		if w.done == done && !w.doneClosed { // still this delivery, and not already closed
			close(w.done)
			w.doneClosed = true
		}
		w.mu.Unlock()
	}
}

// Cut moves the write deadline to now for the delivery in progress, so a write blocked on a
// client that has gone away fails at once instead of draining until its per-chunk deadline.
// Later writes of the same delivery stay cut. It has no effect outside a delivery, so a cut
// that races a clean finish cannot disturb the idle stream that follows.
//
// A write deadline is a capability scoped to the handler's lifetime (see the package
// comment), so Cut must be called only from the handler goroutine, or from a goroutine that
// provably cannot outlive the handler. The producer goroutine must never call it.
func (w *Writer) Cut() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.active || w.cut {
		return
	}
	w.cut = true
	_ = w.rc.SetWriteDeadline(w.now())
}

// Unwrap exposes the wrapped ResponseWriter so http.NewResponseController and other wrappers
// can reach the underlying connection through this one.
func (w *Writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Write slices p into chunks and arms the per-chunk deadline before each.
func (w *Writer) Write(p []byte) (int, error) {
	if !w.isActive() || len(p) == 0 {
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
	if !w.isActive() || len(s) == 0 {
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

// Flush flushes buffered bytes and, at the end-of-delivery flush (after End), clears the
// deadline so later traffic on a persistent stream is not governed by it. The clear runs under
// the lock and only for the delivery that was active when Flush was entered -- the state is
// sampled BEFORE flushing, so a delivery that begins during the flush is untouched, and a stale
// flush cannot wipe a newer delivery's deadline. Flush runs on the handler goroutine, within
// the connection's lifetime. The teardown is deferred, so it frees the slot and clears the
// deadline even if the flush itself is unsupported, fails, or panics.
func (w *Writer) Flush() {
	w.mu.Lock()
	active, ending, gen := w.active, w.ending, w.gen
	w.mu.Unlock()

	if active && ending {
		defer w.teardown(gen)
	}
	_ = w.rc.Flush() // best-effort; dispatches to the first Flusher in the Unwrap chain
}

// teardown ends the delivery identified by gen: it clears the deadline and releases the waiter.
func (w *Writer) teardown(gen uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gen != gen || !w.active {
		return // a newer delivery began, or this one was already ended
	}
	w.active = false
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

func (w *Writer) isActive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active
}

// arm sets the write deadline for the next chunk to now + (time to send n bytes at the
// throughput floor) + slack, capped by the delivery's absolute deadline (msgStart + maxHold).
// It never shortens an existing deadline within a delivery and skips trivial changes. The
// deadline is recorded as current only when the underlying SetWriteDeadline succeeds, so a
// transient failure does not leave the writer believing it has armed a deadline it has not.
func (w *Writer) arm(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.active || w.cut {
		return
	}
	now := w.now()
	if w.msgStart.IsZero() {
		w.msgStart = now
	}
	budget := time.Duration(n)*time.Second/time.Duration(minBytesPerSec) + slack
	dl := now.Add(budget)
	if w.maxHold > 0 {
		if capDL := w.msgStart.Add(w.maxHold); dl.After(capDL) {
			dl = capDL
		}
	}
	if dl.After(w.lastDeadline) && dl.Sub(w.lastDeadline) >= minExtension {
		if err := w.rc.SetWriteDeadline(dl); err == nil {
			w.lastDeadline = dl
		}
	}
}

var (
	_ http.ResponseWriter = (*Writer)(nil)
	_ http.Flusher        = (*Writer)(nil)
	_ io.StringWriter     = (*Writer)(nil)
)
