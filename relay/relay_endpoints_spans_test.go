package relay

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"

	ct "github.com/launchdarkly/go-configtypes"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installSpanRecorder registers a SpanRecorder-backed TracerProvider as the global OTel
// provider and restores the previous provider on cleanup. Both tracing.Tracer() and the
// otelmux middleware resolve through the global provider, so spans produced while handling
// a request are captured by the returned recorder.
//
// Because the tracer provider is process-global, tests that call this must not run in
// parallel with one another (none of the relay tests use t.Parallel).
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
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

func spansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

func requireSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	matches := spansNamed(spans, name)
	require.Lenf(t, matches, 1, "expected exactly one %q span, found %d", name, len(matches))
	return matches[0]
}

// rootSpan returns the single span with no local parent. For a request driven through the
// relay this is the otelmux request span, which parents every manual span in the handler.
func rootSpan(t *testing.T, spans []sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	t.Helper()
	var roots []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if !s.Parent().IsValid() {
			roots = append(roots, s)
		}
	}
	require.Lenf(t, roots, 1, "expected exactly one root span, found %d", len(roots))
	return roots[0]
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	attrs := make(map[attribute.Key]attribute.Value)
	for _, kv := range s.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

// assertChildOfEnded verifies that child is an ended span belonging to parent's trace and
// pointing at parent as its parent span.
func assertChildOfEnded(t *testing.T, child, parent sdktrace.ReadOnlySpan) {
	t.Helper()
	assert.Falsef(t, child.EndTime().IsZero(), "%q span was not ended", child.Name())
	assert.Equalf(t, parent.SpanContext().TraceID(), child.SpanContext().TraceID(),
		"%q should share a trace with %q", child.Name(), parent.Name())
	assert.Equalf(t, parent.SpanContext().SpanID(), child.Parent().SpanID(),
		"%q should be a child of %q", child.Name(), parent.Name())
}

// countStarted reports how many spans with the given name were started. A span that was
// started but never ended (a leak) still appears here, so comparing against the ended
// count detects leaks.
func countStarted(spans []sdktrace.ReadWriteSpan, name string) int {
	n := 0
	for _, s := range spans {
		if s.Name() == name {
			n++
		}
	}
	return n
}

// TestPollingEndpointSpansAreRecorded drives each instrumented polling handler through the
// full relay (so the otelmux request span is present as a real parent) and asserts that the
// serialize and response-write spans exist, are ended, are children of the request span, and
// carry plausible attribute values.
func TestPollingEndpointSpansAreRecorded(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithAllCredentials)

	withStartedRelay(t, config, func(p relayTestParams) {
		contextJSON := []byte(`{"kind":"user","key":"me"}`)
		contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

		serverSDKKey := st.EnvMain.Config.SDKKey
		mobileKey := st.EnvWithAllCredentials.Config.MobileKey

		cases := []struct {
			name     string
			buildReq func() *http.Request
			// countKey is the attribute expected to hold the item/event count on the
			// serialize span, or "" when the handler records no count.
			countKey attribute.Key
			// streamed handlers encode directly to the network, so the serialize span
			// also covers the response write and no separate write span exists.
			streamed bool
		}{
			{
				name: "pollHandlerV2 GET /sdk/poll",
				buildReq: func() *http.Request {
					return st.BuildRequestWithAuth("GET", "/sdk/poll", serverSDKKey, nil)
				},
				countKey: tracing.PayloadEventsKey,
			},
			{
				name: "pollEvalHandlerV2Shared GET /sdk/poll/eval",
				buildReq: func() *http.Request {
					return st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
				},
				countKey: tracing.PayloadEventsKey,
				streamed: true,
			},
			{
				name: "evaluateAllShared REPORT /sdk/evalx/context",
				buildReq: func() *http.Request {
					h := make(http.Header)
					h.Set("Authorization", string(serverSDKKey))
					h.Set("Content-Type", "application/json")
					return st.BuildRequest("REPORT", "/sdk/evalx/context", contextJSON, h)
				},
				countKey: tracing.FlagCountKey,
			},
			{
				name: "pollAllFlagsHandler GET /sdk/flags",
				buildReq: func() *http.Request {
					return st.BuildRequestWithAuth("GET", "/sdk/flags", serverSDKKey, nil)
				},
				countKey: tracing.FlagCountKey,
			},
			{
				name: "pollFlagOrSegment GET /sdk/flags/{key}",
				buildReq: func() *http.Request {
					return st.BuildRequestWithAuth("GET", "/sdk/flags/"+st.Flag1ServerSide.Flag.Key, serverSDKKey, nil)
				},
				countKey: "",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				recorder.Reset()

				result, _ := st.DoRequest(tc.buildReq(), p.relay)
				require.Equal(t, http.StatusOK, result.StatusCode)

				spans := recorder.Ended()
				root := rootSpan(t, spans)
				serialize := requireSpan(t, spans, tracing.SpanSerializePayload)
				assertChildOfEnded(t, serialize, root)
				serializeAttrs := spanAttrs(serialize)

				payloadBytes, ok := serializeAttrs[tracing.PayloadBytesKey]
				require.True(t, ok, "serialize span is missing the payload bytes attribute")
				assert.Positive(t, payloadBytes.AsInt64())

				var responseBytes attribute.Value
				if tc.streamed {
					assert.Empty(t, spansNamed(spans, tracing.SpanWriteResponse),
						"streamed handler should not emit a separate write span")
					streamedAttr, ok := serializeAttrs[tracing.PayloadStreamedKey]
					require.True(t, ok, "streamed serialize span is missing the streamed attribute")
					assert.True(t, streamedAttr.AsBool())
					responseBytes, ok = serializeAttrs[tracing.ResponseBytesKey]
					require.True(t, ok, "streamed serialize span is missing the response bytes attribute")
				} else {
					write := requireSpan(t, spans, tracing.SpanWriteResponse)
					assertChildOfEnded(t, write, root)
					responseBytes, ok = spanAttrs(write)[tracing.ResponseBytesKey]
					require.True(t, ok, "write span is missing the response bytes attribute")
				}
				assert.Equal(t, payloadBytes.AsInt64(), responseBytes.AsInt64(),
					"response bytes should equal the serialized payload size")

				if tc.countKey != "" {
					count, ok := serializeAttrs[tc.countKey]
					require.Truef(t, ok, "serialize span is missing the %q attribute", tc.countKey)
					assert.GreaterOrEqual(t, count.AsInt64(), int64(1))
				} else {
					_, hasEvents := serializeAttrs[tracing.PayloadEventsKey]
					_, hasFlags := serializeAttrs[tracing.FlagCountKey]
					assert.False(t, hasEvents, "single-item serialize span should record no event count")
					assert.False(t, hasFlags, "single-item serialize span should record no flag count")
				}

				// No span may leak: every serialize/write span that was started must also
				// have ended.
				started := recorder.Started()
				assert.Equal(t, 1, countStarted(started, tracing.SpanSerializePayload))
				expectedWriteSpans := 1
				if tc.streamed {
					expectedWriteSpans = 0
				}
				assert.Equal(t, expectedWriteSpans, countStarted(started, tracing.SpanWriteResponse))
			})
		}
	})
}

// TestPollPayloadCacheAcrossRequests verifies that repeated polls for the same basis
// are served from the payload cache — identical bytes, with the serialize span
// reporting a miss on the first request and a hit on the second — and that an
// If-None-Match request short-circuits before any serialize span is started.
func TestPollPayloadCacheAcrossRequests(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		serverSDKKey := st.EnvMain.Config.SDKKey
		poll := func() (*http.Response, []byte) {
			result, body := st.DoRequest(st.BuildRequestWithAuth("GET", "/sdk/poll", serverSDKKey, nil), p.relay)
			return result, body
		}

		cacheHitAttr := func() bool {
			serialize := requireSpan(t, recorder.Ended(), tracing.SpanSerializePayload)
			attr, ok := spanAttrs(serialize)[tracing.PayloadCacheHitKey]
			require.True(t, ok, "serialize span is missing the cache_hit attribute")
			return attr.AsBool()
		}

		recorder.Reset()
		first, firstBody := poll()
		require.Equal(t, http.StatusOK, first.StatusCode)
		assert.False(t, cacheHitAttr(), "first poll should build the payload")

		recorder.Reset()
		second, secondBody := poll()
		require.Equal(t, http.StatusOK, second.StatusCode)
		assert.True(t, cacheHitAttr(), "second poll should be served from the cache")
		assert.Equal(t, firstBody, secondBody, "cached poll should return identical bytes")

		recorder.Reset()
		req := st.BuildRequestWithAuth("GET", "/sdk/poll", serverSDKKey, nil)
		req.Header.Set("If-None-Match", first.Header.Get("Etag"))
		notModified, _ := st.DoRequest(req, p.relay)
		require.Equal(t, http.StatusNotModified, notModified.StatusCode)
		assert.Zero(t, countStarted(recorder.Started(), tracing.SpanSerializePayload),
			"an If-None-Match match should be answered before serialization")
		assert.Zero(t, countStarted(recorder.Started(), tracing.SpanWriteResponse))
	})
}

// TestPollingEndpointSpansDoNotLeakOnEarlyReturn exercises early-return paths that occur
// before serialization and asserts that no serialize or response-write span is left
// dangling. pollFlagOrSegment's not-found path additionally shows that a store span created
// before the early return is properly ended.
//
// Note: the serialize span's own error branches (a json.Marshal failure or a store type-cast
// failure) operate on data the store has already validated and cannot be provoked through the
// test harness, which always serves a fixed in-memory dataset. Those branches are guarded
// structurally by the IIFE's `defer span.End()`, so the span cannot leak regardless of which
// return fires.
func TestPollingEndpointSpansDoNotLeakOnEarlyReturn(t *testing.T) {
	recorder := installSpanRecorder(t)

	t.Run("pollFlagOrSegment not found ends store span and starts no serialize/write span", func(t *testing.T) {
		recorder.Reset()

		envCtx := testenv.MakeTestContextWithData()
		req := buildPreRoutedRequest("GET", nil, nil, map[string]string{"key": "no-such-flag"}, envCtx)
		ctx, parent := tracing.Tracer().Start(req.Context(), "test.request")
		req = req.WithContext(ctx)

		resp := httptest.NewRecorder()
		pollFlagHandler(resp, req)
		parent.End()

		assert.Equal(t, http.StatusNotFound, resp.Code)

		ended := recorder.Ended()
		storeSpan := requireSpan(t, ended, tracing.SpanStoreGet)
		assertChildOfEnded(t, storeSpan, rootSpan(t, ended))

		assert.Empty(t, spansNamed(ended, tracing.SpanSerializePayload))
		assert.Empty(t, spansNamed(ended, tracing.SpanWriteResponse))
		started := recorder.Started()
		assert.Zero(t, countStarted(started, tracing.SpanSerializePayload))
		assert.Zero(t, countStarted(started, tracing.SpanWriteResponse))
	})

	t.Run("pollEvalHandlerV2Shared invalid context starts no serialize/write span", func(t *testing.T) {
		recorder.Reset()

		envCtx := testenv.MakeTestContextWithData()
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		req := buildPreRoutedRequest("REPORT", []byte(`{}`), headers, nil, envCtx)
		ctx, parent := tracing.Tracer().Start(req.Context(), "test.request")
		req = req.WithContext(ctx)

		resp := httptest.NewRecorder()
		pollEvalHandlerV2Shared(resp, req, ct.OptBase2Bytes{})
		parent.End()

		assert.Equal(t, http.StatusBadRequest, resp.Code)

		started := recorder.Started()
		assert.Zero(t, countStarted(started, tracing.SpanSerializePayload))
		assert.Zero(t, countStarted(started, tracing.SpanWriteResponse))
	})
}
