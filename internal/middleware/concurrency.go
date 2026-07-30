package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/util"
)

type initLimiterCtxKey struct{}

type initLimiterHolder struct {
	limiter *concurrency.Limiter
	timeout time.Duration
}

// AcquireInitSlot draws one slot from the shared initialization-delivery limiter for the
// current request, holding it until the returned release is called. On success it also arms
// read and write deadlines so a slow or non-reading client cannot park the slot, and release
// clears them. If the budget is full it writes a 503 (with a jittered Retry-After and a JSON
// body) and returns ok=false; the caller must then write nothing further. A disabled or nil
// limiter always admits with a no-op release, so callers can invoke this unconditionally.
//
// It is called both by LimitConcurrency (which gates a whole handler, e.g. the FDv1 all-flags
// poll) and directly by the FDv2 poll handlers, which acquire only on the full-basis branch so
// a cheap up-to-date reply is never charged.
func AcquireInitSlot(
	limiter *concurrency.Limiter,
	ioTimeout time.Duration,
	w http.ResponseWriter,
	r *http.Request,
) (release func(), ok bool) {
	if !limiter.Enabled() {
		return func() {}, true
	}
	rel, ok := limiter.Acquire(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds()))
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(util.ErrorJSONMsg("relay initialization concurrency limit reached; retry shortly"))
		return func() {}, false
	}

	var clearDeadlines func()
	if ioTimeout > 0 {
		// Bound how long reading the request body or writing the response may block while the
		// slot is held. The deadlines are cleared on release so a kept-alive connection is not
		// affected. Best effort: not all servers support deadlines.
		rc := http.NewResponseController(w)
		deadline := time.Now().Add(ioTimeout)
		_ = rc.SetReadDeadline(deadline)
		_ = rc.SetWriteDeadline(deadline)
		clearDeadlines = func() {
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
	}

	return func() {
		if clearDeadlines != nil {
			clearDeadlines()
		}
		rel()
	}, true
}

// LimitConcurrency wraps a handler with the shared initialization-delivery limiter for a
// request whose whole response is a full-dataset delivery (the FDv1 all-flags poll). The slot
// is acquired on entry and held until the handler returns. On shed it responds 503 and does
// not invoke the wrapped handler. A disabled or nil limiter is a pass-through with zero
// overhead. Handlers that have a cheap no-payload branch (the FDv2 polls) should instead call
// AcquireInitSlot directly, after that branch, so the cheap reply is not charged.
func LimitConcurrency(limiter *concurrency.Limiter, ioTimeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !limiter.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			release, ok := AcquireInitSlot(limiter, ioTimeout, w, r)
			if !ok {
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}

// ProvideInitLimiter makes the shared initialization-delivery limiter available to a
// downstream handler via the request context, without acquiring a slot. It is used for the
// FDv2 poll endpoints, whose handlers acquire lazily (via AcquireInitSlotFromContext) only on
// the full-basis branch, so a cheap up-to-date reply is never charged. Unlike LimitConcurrency
// it is always installed even when the limiter is disabled, since it only stashes a value.
func ProvideInitLimiter(limiter *concurrency.Limiter, ioTimeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), initLimiterCtxKey{}, initLimiterHolder{limiter: limiter, timeout: ioTimeout})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AcquireInitSlotFromContext acquires a slot from the limiter installed by ProvideInitLimiter,
// with the same semantics as AcquireInitSlot. If no limiter was provided (or it is disabled)
// it admits with a no-op release, so a handler can call it unconditionally on its full-basis
// path.
func AcquireInitSlotFromContext(w http.ResponseWriter, r *http.Request) (release func(), ok bool) {
	holder, _ := r.Context().Value(initLimiterCtxKey{}).(initLimiterHolder)
	return AcquireInitSlot(holder.limiter, holder.timeout, w, r)
}

// retryAfterSeconds returns a small jittered Retry-After (in seconds) so a shed herd does
// not retry in lockstep and re-synchronize the next burst. The jitter is derived from the
// arrival time, which naturally spreads concurrent callers.
func retryAfterSeconds() int {
	return 2 + int(time.Now().UnixNano()%4) // 2..5 seconds
}
