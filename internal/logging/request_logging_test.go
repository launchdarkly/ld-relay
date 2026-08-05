package logging

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"
)

func TestRequestLoggerMiddlewareNonStreaming(t *testing.T) {
	logger, mockHandler := logtest.NewMockLogger()
	handler := RequestLoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ab"))
		w.Write([]byte("c"))
	}))

	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/url", nil)
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
	assert.Equal(t, "abc", string(rr.Body.Bytes()))

	assert.True(t, mockHandler.HasMessage(slog.LevelDebug, "request completed"))
	entries := mockHandler.EntriesForLevel(slog.LevelDebug)
	require.NotEmpty(t, entries)
	found := false
	for _, e := range entries {
		if e.Message == "request completed" {
			assert.Equal(t, "GET", e.Attrs["method"])
			assert.Equal(t, "/url", e.Attrs["url"])
			assert.Equal(t, "n/a", e.Attrs["auth"])
			assert.Equal(t, int64(200), e.Attrs["status"])
			assert.Equal(t, uint64(3), e.Attrs["bytes"])
			found = true
		}
	}
	assert.True(t, found, "expected to find 'request completed' log entry")
}

func TestRequestLoggerMiddlewareStreaming(t *testing.T) {
	logger, mockHandler := logtest.NewMockLogger()
	handler := RequestLoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("ab"))
		w.(http.Flusher).Flush()
		w.Write([]byte("c"))
		w.(http.Flusher).Flush()
	}))

	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/url", nil)
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
	assert.Equal(t, "abc", string(rr.Body.Bytes()))

	assert.True(t, mockHandler.HasMessage(slog.LevelDebug, "request started"))
	assert.True(t, mockHandler.HasMessage(slog.LevelDebug, "stream closed"))

	entries := mockHandler.EntriesForLevel(slog.LevelDebug)
	var foundStart, foundClose bool
	for _, e := range entries {
		if e.Message == "request started" {
			assert.Equal(t, "GET", e.Attrs["method"])
			assert.Equal(t, "/url", e.Attrs["url"])
			assert.Equal(t, "n/a", e.Attrs["auth"])
			assert.Equal(t, int64(200), e.Attrs["status"])
			assert.Equal(t, true, e.Attrs["streaming"])
			foundStart = true
		}
		if e.Message == "stream closed" {
			assert.Equal(t, "/url", e.Attrs["url"])
			assert.Equal(t, "n/a", e.Attrs["auth"])
			assert.Equal(t, uint64(3), e.Attrs["bytes"])
			foundClose = true
		}
	}
	assert.True(t, foundStart, "expected to find 'request started' log entry")
	assert.True(t, foundClose, "expected to find 'stream closed' log entry")
}

func TestRequestLoggerMiddlewareAuth(t *testing.T) {
	logger, mockHandler := logtest.NewMockLogger()
	handler := RequestLoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("abc"))
	}))

	rr := httptest.NewRecorder()
	req1, _ := http.NewRequest("GET", "/url", nil)
	req1.Header.Set("Authorization", "abcdefghij")
	handler.ServeHTTP(rr, req1)
	req2, _ := http.NewRequest("GET", "/url", nil)
	req2.Header.Set("Authorization", "abcd")
	handler.ServeHTTP(rr, req2)

	entries := mockHandler.EntriesForLevel(slog.LevelDebug)
	var foundRedacted, foundShort bool
	for _, e := range entries {
		if e.Message == "request completed" {
			if e.Attrs["auth"] == "*fghij" {
				foundRedacted = true
			}
			if e.Attrs["auth"] == "abcd" {
				foundShort = true
			}
		}
	}
	assert.True(t, foundRedacted, "expected to find log entry with redacted auth '*fghij'")
	assert.True(t, foundShort, "expected to find log entry with short auth 'abcd'")
}

// deadlineProbeWriter records whether SetWriteDeadline reached it through a wrapper chain.
type deadlineProbeWriter struct {
	http.ResponseWriter
	gotWriteDeadline bool
}

func (d *deadlineProbeWriter) SetWriteDeadline(time.Time) error {
	d.gotWriteDeadline = true
	return nil
}

// TestLoggingResponseWriterUnwrapExposesConnectionDeadline guards that the request-logging
// wrapper implements Unwrap, so http.NewResponseController can reach the underlying
// connection through it. Without this, deadlines set downstream (e.g. by the init-delivery
// limiter) would silently become no-ops for every request while request logging is enabled.
func TestLoggingResponseWriterUnwrapExposesConnectionDeadline(t *testing.T) {
	base := &deadlineProbeWriter{ResponseWriter: httptest.NewRecorder()}
	lw := &loggingHTTPResponseWriter{logger: slog.Default(), writer: base}
	err := http.NewResponseController(lw).SetWriteDeadline(time.Now().Add(time.Second))
	assert.NoError(t, err, "controller could not set a deadline through loggingHTTPResponseWriter")
	assert.True(t, base.gotWriteDeadline, "SetWriteDeadline did not reach the base writer through loggingHTTPResponseWriter")
}

// stringWriterProbe records whether a write reached it through the io.StringWriter fast
// path rather than being copied to a []byte for Write.
type stringWriterProbe struct {
	*httptest.ResponseRecorder
	gotWriteString bool
}

func (p *stringWriterProbe) WriteString(s string) (int, error) {
	p.gotWriteString = true
	return p.ResponseRecorder.WriteString(s)
}

// TestLoggingWriterPreservesStringWriterFastPath guards the WriteString forwarding that
// keeps a large string payload (an initialization delivery) from being copied into a
// fresh []byte at this hop, while keeping the same status and byte-count bookkeeping as
// the Write path.
func TestLoggingWriterPreservesStringWriterFastPath(t *testing.T) {
	logger, _ := logtest.NewMockLogger()
	probe := &stringWriterProbe{ResponseRecorder: httptest.NewRecorder()}
	req, _ := http.NewRequest("GET", "/url", nil)
	w := &loggingHTTPResponseWriter{logger: logger, writer: probe, request: req}

	n, err := io.WriteString(w, "payload")
	require.NoError(t, err)
	assert.Equal(t, len("payload"), n)
	assert.True(t, probe.gotWriteString, "WriteString did not reach the underlying writer; the payload was copied at this hop")
	assert.Equal(t, "payload", probe.Body.String())
	assert.Equal(t, 200, w.statusCode)
	assert.Equal(t, uint64(len("payload")), w.bytesWritten)
}
