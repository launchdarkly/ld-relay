// Package initwrite provides a progress-aware write deadline for initialization-delivery
// responses -- SSE stream replays and poll responses. It bounds how long a slow or stalled
// client can hold a budget slot without false-killing a slow-but-steady client: a
// minimum-throughput floor drives a per-write deadline, and an absolute cap bounds a single
// delivery.
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
//     deadline is a self-firing timer that would reset an otherwise idle stream. A producer that
//     ends a delivery on an error or cancellation path -- without a final flush -- must call
//     Finish so the deadline never outlives the delivery.
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
// underlying connection while a delivery is active.
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
	// done is closed once the current gated delivery's last byte has been flushed. It is never
	// nil: outside a delivery it is a closed channel, so a producer waiting on it releases its
	// slot immediately rather than blocking forever. doneClosed guards against a double close.
	done       chan struct{}
	doneClosed bool
}

// Wrap returns a Writer for a poll (request/response) delivery: the deadline is armed on every
// write. net/http resets the connection deadline when the handler returns.
func Wrap(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return newWriter(w, maxHold, true)
}

// WrapGated returns a Writer for a persistent stream: it arms nothing until Begin, and clears
// the deadline at the end-of-delivery flush after End (or when Finish is called).
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
	if !w.doneClosed {
		close(w.done)
	}
	w.done = make(chan struct{})
	w.doneClosed = false
}

// Done returns a channel closed once the gated delivery's last byte has been flushed to the
// client. The producer waits on it (or on the request context) before releasing the budget
// slot, so the slot is held for the actual send rather than only until the channel handoff.
// The channel is never nil: with no delivery in progress it is already closed, so a producer
// that waits on it is not left blocked.
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

// Finish ends the current gated delivery unconditionally: it clears any armed write deadline
// and closes the Done channel. It is idempotent and safe to call on any producer exit path,
// and is the backstop for a delivery that ends without a final flush (an error or a cancelled
// context). On the normal path the end-of-delivery flush has already done this, so Finish is a
// no-op. Because it clears the deadline, a producer must call it only once the delivery is
// truly over, not mid-send.
func (w *Writer) Finish() {
	w.mu.Lock()
	wasActive := w.active
	w.active = false
	w.ending = false
	if !w.doneClosed {
		close(w.done)
		w.doneClosed = true
	}
	w.mu.Unlock()
	if wasActive {
		_ = w.rc.SetWriteDeadline(time.Time{})
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
	}
	return total, nil
}

// Flush bounds the flush under the deadline and, at the end-of-delivery flush, clears the
// deadline so later traffic on a persistent stream is not governed by it.
func (w *Writer) Flush() {
	f, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	w.mu.Lock()
	active, ending := w.active, w.ending
	w.mu.Unlock()
	if active {
		w.arm(0)
	}
	f.Flush()
	if active && ending {
		w.mu.Lock()
		w.active = false
		if !w.doneClosed {
			close(w.done) // the basis is fully written; let the producer release the slot
			w.doneClosed = true
		}
		w.mu.Unlock()
		_ = w.rc.SetWriteDeadline(time.Time{})
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
// transient failure does not leave the writer thinking it has armed a deadline it has not.
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
