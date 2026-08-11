package tracing

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

// SingleflightDo runs fn through group under key and annotates the span in ctx with how the
// flight resolved: SingleflightSharedKey reports whether the result was handed to multiple
// callers, and SingleflightWaitMSKey records, only on a caller that waited for a flight another
// caller was already executing, how long it waited. That wait is additionally emitted as a
// SpanSingleflightWait child span covering exactly the waiting window, so a trace's timeline
// shows a labeled bar instead of an unexplained gap. The caller that executed fn records
// neither: it did not wait, and its work is expected to show up as the child spans fn starts on
// that caller's context.
func SingleflightDo(
	ctx context.Context,
	group *singleflight.Group,
	key string,
	fn func() (any, error),
) (any, error) {
	executed := false
	start := time.Now()
	data, err, shared := group.Do(key, func() (any, error) {
		executed = true
		return fn()
	})

	span := trace.SpanFromContext(ctx)
	span.SetAttributes(SingleflightSharedKey.Bool(shared))
	if !executed {
		end := time.Now()
		span.SetAttributes(SingleflightWaitMSKey.Float64(
			float64(end.Sub(start)) / float64(time.Millisecond)))
		// The wait span is back-dated: whether this caller waited (rather than executed) is
		// only known once Do returns, so it cannot be opened beforehand without also giving
		// the executing caller a bogus wait span. It comes from the provider that owns the
		// surrounding span, so it lands wherever that span is recorded.
		tr := span.TracerProvider().Tracer(TracerName)
		_, waitSpan := tr.Start(ctx, SpanSingleflightWait, trace.WithTimestamp(start))
		waitSpan.End(trace.WithTimestamp(end))
	}

	return data, err
}
