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
// A gated delivery ends in one of two ways. On a clean finish the producer calls End and the
// handler's end-of-delivery flush clears the deadline, leaving the now-idle stream alone. If the
// client goes away first, WaitAndFinish returns on context cancellation without touching the
// connection: the per-write deadline already armed bounds anything still in flight, and the
// connection teardown clears it.
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

// End signals that the producer has handed off the whole delivery. The deadline is cleared at
// the next flush -- the eventsource server flushes exactly once at end-of-batch -- so it is
// called before the producer closes the batch channel, guaranteeing that flush observes it.
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
// has returned. Call it after Begin. It must be given the request's context (or another that is
// cancelled when the client disconnects); a never-cancelled context would block the producer,
// and its slot, until the delivery flushes.
func (w *Writer) WaitAndFinish(ctx context.Context) {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()

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
// the lock and only for the delivery that was active when Flush was entered, so it cannot wipe
// the deadline of a newer delivery that began in the meantime. Flush runs on the handler
// goroutine, within the connection's lifetime.
func (w *Writer) Flush() {
	f, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	w.mu.Lock()
	active, ending, gen := w.active, w.ending, w.gen
	w.mu.Unlock()

	f.Flush()

	if !(active && ending) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gen != gen || !w.active {
		return // a newer delivery began, or this one was already ended
	}
	w.active = false
	_ = w.rc.SetWriteDeadline(time.Time{})
	if !w.doneClosed {
		close(w.done) // release the producer only after the deadline is cleared
		w.doneClosed = true
	}
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
	if !w.active {
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
