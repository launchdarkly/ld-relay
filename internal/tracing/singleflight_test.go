package tracing

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/sync/singleflight"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attrsOf(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := make(map[attribute.Key]attribute.Value)
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestSingleflightDoAnnotatesALoneCaller(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	var group singleflight.Group
	ctx, span := provider.Tracer("test").Start(context.Background(), "caller")
	data, err := SingleflightDo(ctx, &group, "key", func() (any, error) { return "result", nil })
	span.End()

	require.NoError(t, err)
	assert.Equal(t, "result", data)

	ended := recorder.Ended()
	require.Len(t, ended, 1, "a caller that executed the function should produce no wait span")
	attrs := attrsOf(ended[0])

	shared, ok := attrs[SingleflightSharedKey]
	require.True(t, ok, "the span should always report whether the flight was shared")
	assert.False(t, shared.AsBool())

	_, waited := attrs[SingleflightWaitMSKey]
	assert.False(t, waited, "the caller executed the function itself, so it should record no wait")
}

func TestSingleflightDoAnnotatesSharedCallers(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("test")

	var group singleflight.Group
	entered := make(chan struct{})
	release := make(chan struct{})

	run := func(name string, fn func() (any, error)) chan error {
		done := make(chan error, 1)
		go func() {
			ctx, span := tracer.Start(context.Background(), name)
			data, err := SingleflightDo(ctx, &group, "key", fn)
			span.End()
			if err == nil && data != "result" {
				err = assert.AnError
			}
			done <- err
		}()
		return done
	}

	// The leader enters the function and blocks there, holding its flight open.
	leaderDone := run("leader", func() (any, error) {
		close(entered)
		<-release
		return "result", nil
	})
	<-entered

	// The follower joins the leader's flight as long as it calls Do before the leader's
	// function returns, which cannot happen until release is closed. The sleep is generous
	// time for the goroutine to get there; if it somehow arrived late, its own function would
	// run and the error below would fail the test loudly.
	followerDone := run("follower", func() (any, error) {
		return nil, assert.AnError
	})
	time.Sleep(100 * time.Millisecond)
	close(release)

	require.NoError(t, <-leaderDone)
	require.NoError(t, <-followerDone)

	ended := recorder.Ended()
	require.Len(t, ended, 3, "expected the leader span, the follower span, and the follower's wait span")

	var followerWaitMS float64
	var waitSpan, followerSpan sdktrace.ReadOnlySpan
	for _, s := range ended {
		attrs := attrsOf(s)
		if s.Name() == SpanSingleflightWait {
			waitSpan = s
			continue
		}

		shared, ok := attrs[SingleflightSharedKey]
		require.True(t, ok, "the span should always report whether the flight was shared")
		assert.True(t, shared.AsBool())

		wait, waited := attrs[SingleflightWaitMSKey]
		switch s.Name() {
		case "leader":
			assert.False(t, waited, "the leader executed the function, so it should record no wait")
		case "follower":
			followerSpan = s
			require.True(t, waited, "the follower waited on the leader's flight, so it should record its wait")
			followerWaitMS = wait.AsFloat64()
			assert.Positive(t, followerWaitMS)
		}
	}

	// The follower's wait is also a span, back-dated to cover exactly the waiting window, so
	// the trace timeline shows the wait instead of a gap.
	require.NotNil(t, waitSpan, "the follower's wait should be visible as a span")
	require.NotNil(t, followerSpan)
	assert.Equal(t, followerSpan.SpanContext().TraceID(), waitSpan.SpanContext().TraceID(),
		"the wait span should live in the follower's trace")
	assert.Equal(t, followerSpan.SpanContext().SpanID(), waitSpan.Parent().SpanID(),
		"the wait span should be a child of the follower's span")
	spanMS := float64(waitSpan.EndTime().Sub(waitSpan.StartTime())) / float64(time.Millisecond)
	assert.InDelta(t, followerWaitMS, spanMS, 0.001,
		"the wait span should cover the same window the wait_ms attribute reports")
}

func TestSingleflightDoToleratesASpanlessContext(t *testing.T) {
	var group singleflight.Group
	data, err := SingleflightDo(context.Background(), &group, "key", func() (any, error) { return "result", nil })
	require.NoError(t, err)
	assert.Equal(t, "result", data)
}
