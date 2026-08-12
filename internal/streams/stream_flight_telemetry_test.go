package streams

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"

	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplayAnnotatesSubscriberSpanWithFlightTelemetry checks that every repository whose replay
// goes through a flight group reports the flight-group telemetry
// (launchdarkly.relay.singleflight.shared, and the wait duration when a replay waits on another's)
// on the subscribing request's span,
// matching what the polling endpoints record. The shared/waiting semantics themselves are covered
// by the tracing package's SingleflightDo tests; these subtests prove each replay call site hands
// the subscriber's context through.
func TestReplayAnnotatesSubscriberSpanWithFlightTelemetry(t *testing.T) {
	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1}, []ldmodel.Segment{testSegment1})

	repos := []struct {
		name string
		repo eventsource.RepositoryWithContext
	}{
		{"server-side v1", &serverSideEnvStreamRepository{store: store, logger: slog.Default()}},
		{"server-side v2", &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true}},
		{"server-side flags only", &serverSideFlagsOnlyEnvStreamRepository{store: store, logger: slog.Default()}},
	}

	for _, tc := range repos {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

			ctx, span := provider.Tracer("test").Start(context.Background(), "test.request")
			eventCh := tc.repo.ReplayWithContext(ctx, "", "")
			for {
				if _, ok, closed := helpers.TryReceive(eventCh, time.Second); closed {
					break
				} else if !ok {
					require.Fail(t, "timed out waiting for replayed event (channel was not closed)")
				}
			}
			span.End()

			ended := recorder.Ended()
			require.Len(t, ended, 1)
			attrs := make(map[attribute.Key]attribute.Value)
			for _, kv := range ended[0].Attributes() {
				attrs[kv.Key] = kv.Value
			}

			shared, ok := attrs[tracing.SingleflightSharedKey]
			require.True(t, ok, "the subscriber's span should report whether the replay build was shared")
			assert.False(t, shared.AsBool(), "a lone replay shares with nobody")

			_, waited := attrs[tracing.SingleflightWaitDurationKey]
			assert.False(t, waited, "a lone replay builds its own payload, so it should record no wait")
		})
	}
}
