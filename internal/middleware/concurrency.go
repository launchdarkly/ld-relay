package middleware

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"
)

// LimitConcurrency wraps a handler with a concurrency limiter keyed by the
// resolved environment, for request-scoped work such as polling. It must be
// applied inside an environment-selecting middleware stack so the EnvContext is
// present on the request. On shed it responds 503 with Retry-After and does not
// invoke the wrapped handler. A disabled or nil limiter is a pass-through.
func LimitConcurrency(limiter *concurrency.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !limiter.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The wait span accounts for queueing time that would otherwise appear as
			// an unattributed gap between the auth span and the handler's spans. It
			// ends as soon as admission is decided, before the handler runs.
			_, span := tracing.Tracer().Start(r.Context(), tracing.SpanConcurrencyWait)
			entryStats := limiter.Stats()
			release, ok := limiter.Acquire(r.Context(), concurrencyEnvKey(r))
			span.SetAttributes(
				tracing.ConcurrencyLimiterKey.String(limiter.Name()),
				tracing.ConcurrencyAdmittedKey.Bool(ok),
				tracing.ConcurrencyHeldKey.Int(entryStats.Held),
				tracing.ConcurrencyWaitingKey.Int(entryStats.Waiting),
				tracing.ConcurrencyMaxKey.Int(entryStats.MaxConcurrent),
			)
			span.End()
			if !ok {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("relay concurrency limit reached; retry shortly"))
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}

// concurrencyEnvKey derives a stable per-environment key for limiter fairness
// from the request's resolved environment. Falls back to empty (one shared
// bucket) if no environment is attached, which should not happen on the routes
// this middleware guards.
func concurrencyEnvKey(r *http.Request) string {
	defer func() { _ = recover() }()
	if info := GetEnvContextInfo(r.Context()); info.Env != nil {
		return info.Env.GetIdentifiers().GetDisplayName()
	}
	return ""
}
