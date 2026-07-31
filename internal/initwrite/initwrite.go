// Package initwrite provides a progress-aware write deadline for initialization-delivery
// responses -- SSE stream replays and poll responses. It bounds how long a slow or stalled
// client can hold a budget slot without false-killing a slow-but-steady client, mirroring
// the streamer service: a minimum-throughput floor drives a per-write deadline, and an
// absolute cap bounds a single delivery.
//
// Two shapes are supported:
//
//   - Poll (Wrap): a request/response delivery. The deadline is armed on every write for the
//     lifetime of the wrapper; net/http clears the connection's deadline when the handler
//     returns, so nothing leaks into the next request on a kept-alive connection.
//   - Stream (WrapGated): a persistent SSE connection, where net/http does NOT clear the
//     deadline between the initial delivery and later delta/heartbeat traffic. The deadline is
//     armed only between Begin and the end-of-delivery flush, and is cleared there, so live
//     traffic and idle periods carry no deadline (important on HTTP/2, where a lingering
//     deadline is a self-firing timer that would reset an idle stream).
//
// A client sustaining at least the throughput floor per write keeps re-arming and is not cut
// for being slow; one that stalls or drops below the floor on a write has its write fail,
// which the HTTP server turns into a closed connection. The absolute cap (maxHold) backstops
// a client that stays at the floor on a very large payload: because the cap governs once a
// delivery would exceed maxHold at the floor (~7.5 MiB at the 64 KiB/s floor and the 2m
// default), such a client is cut at maxHold. See docs/configuration.md.
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
	// time rather than the whole payload.
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

	mu           sync.Mutex
	active       bool
	ending       bool
	msgStart     time.Time
	lastDeadline time.Time
}

// Wrap returns a Writer for a poll (request/response) delivery: the deadline is armed on every
// write. net/http clears the connection deadline when the handler returns.
func Wrap(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return &Writer{ResponseWriter: w, rc: http.NewResponseController(w), maxHold: maxHold, active: true}
}

// WrapGated returns a Writer for a persistent stream: it arms nothing until Begin, and clears
// the deadline at the end-of-delivery flush after End.
func WrapGated(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return &Writer{ResponseWriter: w, rc: http.NewResponseController(w), maxHold: maxHold}
}

// Begin marks the start of a gated delivery; writes from here on arm the deadline. It is
// called from the producer before it starts producing events.
func (w *Writer) Begin() {
	w.mu.Lock()
	w.active = true
	w.ending = false
	w.msgStart = time.Time{}
	w.lastDeadline = time.Time{}
	w.mu.Unlock()
}

// End signals that the producer has handed off the whole delivery. The deadline is cleared at
// the next flush -- the eventsource server flushes exactly once at end-of-batch -- so it is
// called before the producer closes the batch channel, guaranteeing that flush observes it.
func (w *Writer) End() {
	w.mu.Lock()
	w.ending = true
	w.mu.Unlock()
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

// WriteString satisfies io.StringWriter so eventsource's io.WriteString does not allocate a
// []byte copy of the entire payload per connection. It slices the string and copies at most
// one chunk at a time.
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
// It never shortens an existing deadline within a delivery and skips trivial changes.
func (w *Writer) arm(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.active {
		return
	}
	now := time.Now()
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
		_ = w.rc.SetWriteDeadline(dl)
		w.lastDeadline = dl
	}
}

var (
	_ http.ResponseWriter = (*Writer)(nil)
	_ http.Flusher        = (*Writer)(nil)
	_ io.StringWriter     = (*Writer)(nil)
)
