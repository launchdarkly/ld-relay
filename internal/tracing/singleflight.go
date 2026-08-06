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
// caller was already executing, how long it waited. The caller that executed fn records no wait
// time: it did not wait, and its work is expected to show up as the child spans fn starts on
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
		span.SetAttributes(SingleflightWaitMSKey.Float64(
			float64(time.Since(start)) / float64(time.Millisecond)))
	}

	return data, err
}
