package middleware

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
)

// LimitConcurrency wraps a handler with a concurrency limiter for request-scoped
// initialization deliveries such as polling. The slot is held for the whole request
// (acquire on entry, release when the handler returns) and acquired with the request
// context, so a client that disconnects while queued drops out immediately. On shed it
// responds 503 with Retry-After and does not invoke the wrapped handler. A disabled or
// nil limiter is a pass-through with zero overhead.
func LimitConcurrency(limiter *concurrency.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !limiter.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			release, ok := limiter.Acquire(r.Context())
			if !ok {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("relay initialization concurrency limit reached; retry shortly"))
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}
