package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installConcurrencySpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return recorder
}

func waitSpanAttrs(t *testing.T, recorder *tracetest.SpanRecorder) map[attribute.Key]attribute.Value {
	t.Helper()
	var matches []sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == tracing.SpanConcurrencyWait {
			matches = append(matches, s)
		}
	}
	require.Len(t, matches, 1, "expected exactly one wait span")
	attrs := make(map[attribute.Key]attribute.Value)
	for _, kv := range matches[0].Attributes() {
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

func TestLimitConcurrencyEmitsWaitSpan(t *testing.T) {
	recorder := installConcurrencySpanRecorder(t)

	limiter := concurrency.New("test_limiter", concurrency.Params{MaxConcurrent: 1})
	t.Cleanup(limiter.Close)
	handler := LimitConcurrency(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("admitted", func(t *testing.T) {
		recorder.Reset()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusOK, resp.Code)

		attrs := waitSpanAttrs(t, recorder)
		assert.Equal(t, "test_limiter", attrs[tracing.ConcurrencyLimiterKey].AsString())
		assert.True(t, attrs[tracing.ConcurrencyAdmittedKey].AsBool())
		assert.Equal(t, int64(1), attrs[tracing.ConcurrencyMaxKey].AsInt64())
		assert.Equal(t, int64(0), attrs[tracing.ConcurrencyHeldKey].AsInt64())
		assert.Equal(t, int64(0), attrs[tracing.ConcurrencyWaitingKey].AsInt64())
	})

	t.Run("rejected while saturated", func(t *testing.T) {
		recorder.Reset()
		release, ok := limiter.Acquire(context.Background(), "other")
		require.True(t, ok)
		defer release()

		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

		attrs := waitSpanAttrs(t, recorder)
		assert.False(t, attrs[tracing.ConcurrencyAdmittedKey].AsBool())
		assert.Equal(t, int64(1), attrs[tracing.ConcurrencyHeldKey].AsInt64())
	})

	t.Run("disabled limiter emits no span", func(t *testing.T) {
		recorder.Reset()
		disabled := LimitConcurrency(concurrency.New("off", concurrency.Params{}))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
		resp := httptest.NewRecorder()
		disabled.ServeHTTP(resp, httptest.NewRequest("GET", "/", nil))
		assert.Equal(t, http.StatusOK, resp.Code)
		assert.Empty(t, recorder.Ended())
	})
}
