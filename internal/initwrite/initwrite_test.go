package initwrite

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadlineConn is a ResponseWriter that records every write deadline armed on it, so tests
// can assert the progress-aware arming logic without a real socket.
type deadlineConn struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func newDeadlineConn() *deadlineConn { return &deadlineConn{ResponseRecorder: httptest.NewRecorder()} }

func (d *deadlineConn) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}
func (d *deadlineConn) Flush() {}

func TestWriteArmsPerChunkDeadlineUnderCap(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, 2*time.Minute)

	// A payload spanning several chunks should arm a deadline at least once, and every armed
	// deadline must sit within [now+perChunk-ish, msgStart+maxHold].
	start := time.Now()
	n, err := w.Write(make([]byte, 3*chunkSize+1234))
	require.NoError(t, err)
	assert.Equal(t, 3*chunkSize+1234, n)
	require.NotEmpty(t, base.deadlines, "expected at least one write deadline to be armed")
	for _, dl := range base.deadlines {
		assert.False(t, dl.Before(start), "deadline must be in the future")
		assert.False(t, dl.After(start.Add(2*time.Minute+time.Second)), "deadline must be capped by maxHold")
	}
}

func TestWriteCapsDeadlineAtMaxHold(t *testing.T) {
	base := newDeadlineConn()
	// A tiny cap (well under the per-chunk budget) must clamp the armed deadline to the cap.
	w := Wrap(base, 500*time.Millisecond)
	start := time.Now()
	_, err := w.Write(make([]byte, chunkSize))
	require.NoError(t, err)
	require.NotEmpty(t, base.deadlines)
	last := base.deadlines[len(base.deadlines)-1]
	assert.False(t, last.After(start.Add(600*time.Millisecond)), "deadline should be clamped near msgStart+maxHold")
}

func TestUnwrapReachesBase(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, time.Minute)
	// The deadline set through the controller must reach the base connection.
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	assert.NoError(t, err)
	assert.NotEmpty(t, base.deadlines, "SetWriteDeadline did not reach base through initwrite.Writer.Unwrap")
}

func TestEmptyWriteNoDeadline(t *testing.T) {
	base := newDeadlineConn()
	w := Wrap(base, time.Minute)
	_, err := w.Write(nil)
	require.NoError(t, err)
	assert.Empty(t, base.deadlines, "an empty write should not arm a deadline")
}
