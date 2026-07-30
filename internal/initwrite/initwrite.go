// Package initwrite provides a progress-aware write deadline for initialization-delivery
// responses -- SSE stream replays and poll responses. It bounds how long a slow or stalled
// client can hold a budget slot, without false-killing a slow-but-steady client, mirroring
// the approach used by the streamer service: a minimum-throughput floor drives a per-chunk
// deadline, and a separate absolute cap bounds a single delivery.
//
// A client that sustains at least the throughput floor keeps re-arming the per-chunk
// deadline and is never cut for being slow; one that drops below the floor (or stalls) on a
// chunk has its write fail, which the HTTP server turns into a closed connection. The
// absolute cap backstops a client that stays exactly at the floor on a very large payload.
package initwrite

import (
	"net/http"
	"time"
)

const (
	// minBytesPerSec is the throughput floor: a client sustaining at least this rate is never
	// cut for being slow. It matches the streamer service's default.
	minBytesPerSec = 64 * 1024
	// chunkSize bounds how much is written under a single deadline. Large writes are sliced
	// only to re-arm the deadline mid-write; no data is buffered or copied.
	chunkSize = 1 << 20 // 1 MiB
	// slack is added to each per-chunk deadline to tolerate brief stalls without a false cut.
	slack = 5 * time.Second
	// perChunkDeadline is the time budget for one chunk at the throughput floor, plus slack.
	perChunkDeadline = (chunkSize/minBytesPerSec)*time.Second + slack
	// idleReset starts a fresh message (resets the absolute-cap clock) after a gap between
	// writes, so a long-lived stream's periodic deltas are each bounded on their own rather
	// than against the whole connection lifetime.
	idleReset = time.Second
	// minExtension avoids a SetWriteDeadline syscall for a trivially small change.
	minExtension = 100 * time.Millisecond
)

// Writer wraps an http.ResponseWriter and arms a progress-aware write deadline on the
// underlying connection as it writes. maxHold is the absolute cap on a single message's
// total write time; <= 0 disables the cap (only the per-chunk floor applies).
type Writer struct {
	http.ResponseWriter
	rc      *http.ResponseController
	maxHold time.Duration

	msgStart     time.Time
	lastWrite    time.Time
	lastDeadline time.Time
}

// Wrap returns a Writer around w. maxHold is the absolute per-message cap.
func Wrap(w http.ResponseWriter, maxHold time.Duration) *Writer {
	return &Writer{ResponseWriter: w, rc: http.NewResponseController(w), maxHold: maxHold}
}

// Unwrap exposes the wrapped ResponseWriter so http.NewResponseController and other wrappers
// can reach the underlying connection through this one.
func (w *Writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Write slices p into chunks and arms the per-chunk deadline before each, so a write that
// cannot keep up with the throughput floor (or stalls) fails fast, while a slow-but-steady
// write keeps extending its deadline. It buffers nothing.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return w.ResponseWriter.Write(p)
	}
	now := time.Now()
	// A gap since the last write (or the first write) starts a new message: reset the
	// absolute-cap clock and force a fresh deadline.
	if w.msgStart.IsZero() || now.Sub(w.lastWrite) > idleReset {
		w.msgStart = now
		w.lastDeadline = time.Time{}
	}
	total := 0
	for total < len(p) {
		end := min(total+chunkSize, len(p))
		w.arm(time.Now())
		n, err := w.ResponseWriter.Write(p[total:end])
		total += n
		if err != nil {
			w.lastWrite = time.Now()
			return total, err
		}
	}
	w.lastWrite = time.Now()
	return total, nil
}

// arm sets the write deadline for the next chunk to now + perChunkDeadline, capped by the
// message's absolute deadline (msgStart + maxHold). It never shortens an existing deadline
// within a message and skips trivial changes to avoid syscall churn.
func (w *Writer) arm(now time.Time) {
	dl := now.Add(perChunkDeadline)
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

// Flush forwards to the underlying flusher. eventsource requires the writer to be an
// http.Flusher; keeping the method here ensures it does not type-assert past this wrapper.
func (w *Writer) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var (
	_ http.ResponseWriter = (*Writer)(nil)
	_ http.Flusher        = (*Writer)(nil)
)
