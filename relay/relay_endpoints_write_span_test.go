package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httpStatusCodeKey and httpBodySizeKey are the semantic-convention attributes recorded by the
// HTTP tracing middleware on the request span, and by traceWriteResponse on the write span.
const (
	httpStatusCodeKey = attribute.Key("http.response.status_code")
	httpBodySizeKey   = attribute.Key("http.response.body.size")
)

// writeErrorResponseWriter fails every body write, standing in for a client that disconnected
// mid-response. The header still goes out, which is what makes this case interesting: the
// status code the request span reports cannot change afterwards.
type writeErrorResponseWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *writeErrorResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

// TestWriteSpanReportsNotModified covers the ordinary SDK polling path, a conditional request
// whose Etag still matches. No body is written, and the write span says so through its status
// code rather than through a byte count that would have reported the whole payload.
func TestWriteSpanReportsNotModified(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		first := httptest.NewRecorder()
		p.relay.Handler.ServeHTTP(first, st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil))
		require.Equal(t, http.StatusOK, first.Code)
		etag := first.Header().Get("Etag")
		require.NotEmpty(t, etag)

		recorder.Reset()
		conditional := st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil)
		conditional.Header.Set("If-None-Match", etag)
		second := httptest.NewRecorder()

		p.relay.Handler.ServeHTTP(second, conditional)

		require.Equal(t, http.StatusNotModified, second.Code)
		require.Zero(t, second.Body.Len())

		spans := recorder.Ended()
		write := requireSpan(t, spans, tracing.SpanWriteResponse)
		serialize := requireSpan(t, spans, tracing.SpanSerializePayload)

		assert.Equal(t, int64(http.StatusNotModified), spanAttrs(write)[httpStatusCodeKey].AsInt64())
		assert.Equal(t, codes.Unset, write.Status().Code, "a 304 is not an error")

		// The payload was built even though it was not sent, and the serialize span still
		// reports its size. The request span reports no body size at all, since nothing was
		// written.
		assert.Positive(t, spanAttrs(serialize)[tracing.PayloadBytesKey].AsInt64())
		_, hasBodySize := spanAttrs(rootSpan(t, spans))[httpBodySizeKey]
		assert.False(t, hasBodySize, "a 304 writes no body, so no body size should be recorded")
	})
}

// TestWriteSpanUnderCompression pins down the division of labour that replaced the old
// relay.response.bytes attribute: the serialize span reports the payload as built, and the
// request span reports the compressed body that actually went out. Relay's own handlers cannot
// observe the latter, because the ResponseWriter they are handed is the compressing one.
func TestWriteSpanUnderCompression(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.HTTP.EnableCompression = true
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		req := st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()

		p.relay.Handler.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "gzip", w.Header().Get("Content-Encoding"))

		spans := recorder.Ended()
		payloadBytes := spanAttrs(requireSpan(t, spans, tracing.SpanSerializePayload))[tracing.PayloadBytesKey].AsInt64()
		bodySize := spanAttrs(rootSpan(t, spans))[httpBodySizeKey].AsInt64()

		assert.Equal(t, int64(w.Body.Len()), bodySize,
			"the request span should report the compressed body that was actually sent")
		assert.Less(t, bodySize, payloadBytes,
			"the compressed body should be smaller than the serialized payload")
	})
}

// TestWriteSpanRecordsWriteFailure covers a write that fails after the header has gone out. The
// response is truncated, but the status line still says 200, so the request span cannot show
// the failure -- the write span is the only place it appears.
func TestWriteSpanRecordsWriteFailure(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		writeErr := errors.New("connection reset by peer")
		w := &writeErrorResponseWriter{ResponseRecorder: httptest.NewRecorder(), err: writeErr}

		p.relay.Handler.ServeHTTP(w, st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil))

		require.Equal(t, http.StatusOK, w.Code)
		require.Zero(t, w.Body.Len(), "the body write failed, so nothing was recorded")

		spans := recorder.Ended()
		write := requireSpan(t, spans, tracing.SpanWriteResponse)

		assert.Equal(t, codes.Error, write.Status().Code, "a failed write should fail the span")
		assert.Contains(t, write.Status().Description, writeErr.Error())
		require.Len(t, write.Events(), 1, "the write error should be recorded on the span")
		assert.Equal(t, "exception", write.Events()[0].Name)

		// The status code attribute still reports what was sent in the header.
		assert.Equal(t, int64(http.StatusOK), spanAttrs(write)[httpStatusCodeKey].AsInt64())

		// The request span's status is driven by the status code, so it stays unset: this is
		// exactly why the failure has to be recorded on the write span.
		assert.Equal(t, codes.Unset, rootSpan(t, spans).Status().Code)
	})
}
