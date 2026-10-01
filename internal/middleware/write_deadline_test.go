package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadlineRecorder is a ResponseWriter that records the write deadlines set on it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRecorder) recorded() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.deadlines...)
}

var testUnitLimits = initwrite.Limits{MinBytesPerSecond: 1024, Slack: time.Second} //nolint:gochecknoglobals // test-only constant

func writeBody(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write(make([]byte, 2048))
}

// writeLargeBody writes 640 KiB, which is 10s at the 64 KiB/s initialization-delivery floor.
func writeLargeBody(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write(make([]byte, 640*1024))
}

// assertResponseDeadline checks that the response was written under a deadline of about
// start + want, and that the deadline was cleared afterwards.
func assertResponseDeadline(t *testing.T, rec *deadlineRecorder, start time.Time, want time.Duration) {
	t.Helper()
	deadlines := rec.recorded()
	require.Len(t, deadlines, 2)
	assert.WithinDuration(t, start.Add(want), deadlines[0], 500*time.Millisecond)
	assert.True(t, deadlines[1].IsZero(), "the deadline must be cleared when the handler returns")
}

func TestWriteDeadlineWithoutLimitsIsPassThrough(t *testing.T) {
	rec := newDeadlineRecorder()
	WriteDeadline(nil)(http.HandlerFunc(writeBody)).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assert.Empty(t, rec.recorded())
}

func TestWriteDeadlineSetsAndClearsDeadline(t *testing.T) {
	rec := newDeadlineRecorder()
	start := time.Now()
	WriteDeadline(&testUnitLimits)(http.HandlerFunc(writeBody)).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assertResponseDeadline(t, rec, start, 3*time.Second)
}

func TestLimiterMiddlewaresApplyUnitLimits(t *testing.T) {
	disabled := concurrency.New("t", concurrency.Params{MaxConcurrent: 0})
	enabled := concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 4})
	// A floor higher than the initialization-delivery floor of 64 KiB/s, so a limited
	// response shows that it uses the higher floor: 640 KiB at 1 MiB/s is 0.625s, against
	// 10s at 64 KiB/s. The slack stays the initialization-delivery slack.
	highFloor := initwrite.Limits{MinBytesPerSecond: 1 << 20, Slack: time.Second}
	limitedWant := 625*time.Millisecond + initwrite.WriteSlack

	for _, tc := range []struct {
		name string
		mw   func(http.Handler) http.Handler
		body http.HandlerFunc
		want time.Duration
	}{
		{"LimitConcurrency disabled", LimitConcurrency(disabled, time.Minute, nil, &testUnitLimits), writeBody, 3 * time.Second},
		{"ProvideInitLimiter disabled", ProvideInitLimiter(disabled, time.Minute, nil, &testUnitLimits), writeBody, 3 * time.Second},
		{"LimitConcurrency enabled", LimitConcurrency(enabled, time.Minute, nil, &highFloor), writeLargeBody, limitedWant},
		{"ProvideInitLimiter enabled", ProvideInitLimiter(enabled, time.Minute, nil, &highFloor), writeLargeBody, limitedWant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newDeadlineRecorder()
			start := time.Now()
			tc.mw(tc.body).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			assertResponseDeadline(t, rec, start, tc.want)
		})
	}
}
