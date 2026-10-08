package streams

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

type proofDeadlineRec struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
	dl []time.Time
}

func (d *proofDeadlineRec) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dl = append(d.dl, t)
	return nil
}

// A handler that returns normally (for example eventsource on MaxConnTime, server.go:440-442)
// while a unit is in progress (mid replay batch, no end-of-batch flush yet) on HTTP/1. The exit
// defer arms now+slack, then the deferred cancel wakes the watcher, whose Cut overwrites it
// with now. net/http's final flush of the buffered tail then fails at once.
func TestProofExitSlackOverwrittenByWatcherCut(t *testing.T) {
	rec := &proofDeadlineRec{ResponseRecorder: httptest.NewRecorder()}
	unit := initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: 5 * time.Second}
	iw := initwrite.WrapStream(rec, unit, initwrite.Limits{})
	r := httptest.NewRequest("GET", "/", nil) // HTTP/1.1
	ctx, cancel := context.WithCancel(r.Context())
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: partial replay\n\n")) // unit starts, never flushed
	})
	serveWithCut(h, iw, r.WithContext(ctx), cancel, true)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.dl) < 3 {
		t.Fatalf("deadlines: %v", rec.dl)
	}
	exit, last := rec.dl[len(rec.dl)-2], rec.dl[len(rec.dl)-1]
	t.Logf("exit deadline in %v, final deadline in %v", time.Until(exit).Round(time.Millisecond), time.Until(last).Round(time.Millisecond))
	if !(time.Until(exit) > 4*time.Second && time.Until(last) <= 0) {
		t.Fatalf("expected Exit's slack to be overwritten by Cut(now); got %v", rec.dl)
	}
}
