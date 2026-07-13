package middleware

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
)

const (
	// defaultStreamMemoryFraction is the fraction of detected available memory reserved
	// for streaming connection headroom when deriving an automatic default limit.
	defaultStreamMemoryFraction = 0.25
	// assumedBytesPerStream is a conservative planning estimate of memory used per
	// admitted streaming connection, used only to derive the automatic default limit.
	assumedBytesPerStream = 2 * 1024 * 1024
	minAutoDetectedLimit  = 50
	maxAutoDetectedLimit  = 100000
)

// StreamAdmissionController rejects new streaming connections once a concurrency limit
// is reached, returning 503 with Retry-After for connections over the limit before they
// ever reach the handler. A limit of 0 disables the check (unlimited).
type StreamAdmissionController struct {
	limit  int64
	active int64
}

// NewStreamAdmissionController creates a controller with the given concurrent-stream limit.
// If limit is <= 0, a default is derived from detected available memory (the process's
// cgroup limit if set, otherwise total system memory); if memory can't be detected either,
// the controller falls back to unlimited.
func NewStreamAdmissionController(limit int) *StreamAdmissionController {
	if limit <= 0 {
		limit = autoDetectStreamLimit()
	}
	return &StreamAdmissionController{limit: int64(limit)}
}

func autoDetectStreamLimit() int {
	mem, ok := detectAvailableMemoryBytes()
	if !ok {
		return 0
	}
	limit := int(float64(mem) * defaultStreamMemoryFraction / assumedBytesPerStream)
	if limit < minAutoDetectedLimit {
		return minAutoDetectedLimit
	}
	if limit > maxAutoDetectedLimit {
		return maxAutoDetectedLimit
	}
	return limit
}

// ActiveStreams returns the current number of admitted, in-flight streaming connections.
func (c *StreamAdmissionController) ActiveStreams() int64 {
	return atomic.LoadInt64(&c.active)
}

// Limit wraps a streaming handler, rejecting new connections over the configured limit
// with 503 Service Unavailable and a Retry-After header, before the handler runs.
func (c *StreamAdmissionController) Limit(next http.Handler) http.Handler {
	if c.limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := atomic.AddInt64(&c.active, 1)
		if n > c.limit {
			atomic.AddInt64(&c.active, -1)
			recordStreamAdmissionRejected(req)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, `{"error":"relay is at its configured stream connection limit (%d); back off and retry"}`, c.limit)
			return
		}
		defer atomic.AddInt64(&c.active, -1)
		next.ServeHTTP(w, req)
	})
}

func recordStreamAdmissionRejected(req *http.Request) {
	ctx := GetEnvContextInfo(req.Context())
	userAgent := getUserAgent(req)
	sdkWrapper := getSDKWrapper(req)
	metrics.WithCount(ctx.Env.GetMetricsContext(), userAgent, sdkWrapper, func() {}, metrics.StreamAdmissionsRejected)
}
