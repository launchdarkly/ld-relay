package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
	"github.com/launchdarkly/ld-relay/v9/internal/util"
	"go.opentelemetry.io/otel/trace"
)

type initLimiterCtxKey struct{}

type initLimiterHolder struct {
	limiter  *concurrency.Limiter
	maxHold  time.Duration
	recorder InitShedRecorder
}

// InitShedRecorder counts polling requests the initialization-delivery budget shed. The
// metrics package implements it. A nil recorder is permitted and records nothing.
type InitShedRecorder interface {
	RecordPollShed(envName string)
}

// AcquireInitSlot draws one slot from the shared initialization-delivery limiter for the
// current request, holding it until the returned release is called. If the budget is full it
// writes a 503 (with a JSON body) and returns ok=false; the caller
// must then write nothing further. A disabled or nil limiter always admits with a no-op
// release, so callers can invoke this unconditionally.
//
// The response write itself is bounded separately, by the progress-aware writer that
// LimitConcurrency / ProvideInitLimiter install, so a slow client cannot park the slot.
//
// It is called both by LimitConcurrency (which gates a whole handler, e.g. the FDv1 all-flags
// poll) and directly by the FDv2 poll handlers, which acquire only on the full-basis branch so
// a cheap up-to-date reply is never charged.
func AcquireInitSlot(limiter *concurrency.Limiter, recorder InitShedRecorder, w http.ResponseWriter, r *http.Request) (release func(), ok bool) {
	if !limiter.Enabled() {
		return func() {}, true
	}
	// The request span, from the router's tracing middleware, records how the budget
	// treated this request: whether it was admitted, how long it waited for a slot, and,
	// for a rejection, the cause. The span is a no-op when tracing is disabled.
	span := trace.SpanFromContext(r.Context())
	start := time.Now()
	rel, ok := limiter.Acquire(r.Context())
	span.SetAttributes(
		tracing.InitAdmittedKey.Bool(ok),
		tracing.InitQueueWaitDurationKey.Float64(time.Since(start).Seconds()),
	)
	if !ok {
		reason := shedReason(r, limiter)
		span.SetAttributes(tracing.InitShedReasonKey.String(reason))
		// Only budget pressure counts as a shed. A client that left the queue, and a
		// shutdown drain, show on the rejected counter under their own causes.
		if reason == "budget_full" && recorder != nil {
			recorder.RecordPollShed(envNameFromContext(r))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(util.ErrorJSONMsg("relay initialization concurrency limit reached; retry shortly"))
		return func() {}, false
	}
	return rel, true
}

// LimitConcurrency wraps a handler with the shared initialization-delivery limiter for a
// request whose whole response is a full-dataset delivery (the FDv1 all-flags poll). The slot
// is acquired on entry and held until the handler returns, and the response is written through
// a progress-aware writer (see initwrite) so a slow or stalled client cannot park the slot. On
// shed it responds 503 and does not invoke the wrapped handler. A disabled or nil limiter is a
// pass-through with zero overhead. Handlers that have a cheap no-payload branch (the FDv2
// polls) should instead call AcquireInitSlot directly, after that branch, so the cheap reply
// is not charged.
func LimitConcurrency(limiter *concurrency.Limiter, maxHold time.Duration, recorder InitShedRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !limiter.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			release, ok := AcquireInitSlot(limiter, recorder, w, r)
			if !ok {
				return
			}
			defer release()
			defer clearWriteDeadline(w)
			next.ServeHTTP(initwrite.Wrap(w, maxHold), r)
		})
	}
}

// clearWriteDeadline removes any write deadline the progress-aware writer armed on the
// connection, so it cannot linger on a kept-alive connection and fire during a later request.
// This server sets no http.Server.WriteTimeout, so net/http does not reset it for us.
func clearWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

// ProvideInitLimiter makes the shared initialization-delivery limiter available to a
// downstream handler via the request context, without acquiring a slot, and wraps the response
// in the progress-aware writer. It is used for the FDv2 poll endpoints, whose handlers acquire
// lazily (via AcquireInitSlotFromContext) only on the full-basis branch, so a cheap up-to-date
// reply is never charged. A disabled or nil limiter is a pass-through with zero overhead.
func ProvideInitLimiter(limiter *concurrency.Limiter, maxHold time.Duration, recorder InitShedRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !limiter.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer clearWriteDeadline(w)
			ctx := context.WithValue(r.Context(), initLimiterCtxKey{}, initLimiterHolder{limiter: limiter, maxHold: maxHold, recorder: recorder})
			next.ServeHTTP(initwrite.Wrap(w, maxHold), r.WithContext(ctx))
		})
	}
}

// AcquireInitSlotFromContext acquires a slot from the limiter installed by ProvideInitLimiter,
// with the same semantics as AcquireInitSlot. If no limiter was provided (or it is disabled)
// it admits with a no-op release, so a handler can call it unconditionally on its full-basis
// path.
func AcquireInitSlotFromContext(w http.ResponseWriter, r *http.Request) (release func(), ok bool) {
	holder, _ := r.Context().Value(initLimiterCtxKey{}).(initLimiterHolder)
	return AcquireInitSlot(holder.limiter, holder.recorder, w, r)
}

// envNameFromContext returns the display name of the request's environment, or an empty
// string when the context has none. It must not panic: the shed path runs on routes whose
// stacks differ in when they attach the environment.
func envNameFromContext(r *http.Request) string {
	if info, ok := r.Context().Value(contextKey).(EnvContextInfo); ok && info.Env != nil {
		return info.Env.GetIdentifiers().GetDisplayName()
	}
	return ""
}

// shedReason names the cause of a rejection, with the same decision order as the stream
// shed path: a context that already ended means the client is gone, a closed limiter means
// shutdown, and anything else is a full budget.
func shedReason(r *http.Request, limiter *concurrency.Limiter) string {
	if r.Context().Err() != nil {
		return "client_gone"
	}
	if limiter.Closed() {
		return "shutdown"
	}
	return "budget_full"
}
