package middleware

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// StreamAdmissionController rejects new streaming connections once a configured
// concurrency limit is reached, instead of accepting them and letting their initial
// state replay queue up behind eventsource's single distribution goroutine. Under a
// connection storm against large multi-tenant environments, that queuing grows memory
// unboundedly until the process is OOM-killed; this converts that into an immediate,
// explicit 503 for connections over the limit, while connections already admitted
// keep running normally. A limit of 0 disables the check (unlimited, prior behavior).
type StreamAdmissionController struct {
	limit  int64
	active int64
}

// NewStreamAdmissionController creates a controller with the given concurrent-stream limit.
// limit <= 0 means unlimited.
func NewStreamAdmissionController(limit int) *StreamAdmissionController {
	return &StreamAdmissionController{limit: int64(limit)}
}

// ActiveStreams returns the current number of admitted, in-flight streaming connections.
func (c *StreamAdmissionController) ActiveStreams() int64 {
	return atomic.LoadInt64(&c.active)
}

// Limit wraps a streaming handler, rejecting new connections over the configured limit
// with 503 Service Unavailable and a Retry-After header, before the handler (and
// therefore the underlying eventsource subscription) is ever invoked.
func (c *StreamAdmissionController) Limit(next http.Handler) http.Handler {
	if c.limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := atomic.AddInt64(&c.active, 1)
		if n > c.limit {
			atomic.AddInt64(&c.active, -1)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"relay is at its configured stream connection limit (%d); back off and retry"}`, c.limit)
			return
		}
		defer atomic.AddInt64(&c.active, -1)
		next.ServeHTTP(w, req)
	})
}
