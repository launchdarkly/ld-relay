package logging

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
)

func TestRequestLoggerMiddlewareNonStreaming(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	handler := RequestLoggerMiddleware(mockLog.Loggers)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ab"))
		w.Write([]byte("c"))
	}))

	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/url", nil)
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
	assert.Equal(t, "abc", string(rr.Body.Bytes()))

	mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Request: method=GET url=/url auth=n/a status=200 bytes=3")
}

func TestRequestLoggerMiddlewareStreaming(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	handler := RequestLoggerMiddleware(mockLog.Loggers)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Request: method=GET url=/url auth=n/a status=200 \\(streaming\\)")
	mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Stream closed: url=/url auth=n/a bytes=3")
}

func TestRequestLoggerMiddlewareAuth(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	handler := RequestLoggerMiddleware(mockLog.Loggers)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Request: method=GET url=/url auth=\\*fghij status=200 bytes=3")
	mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Request: method=GET url=/url auth=abcd status=200 bytes=3")
}

type deadlineRecordingWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushErr  error
}

func (w *deadlineRecordingWriter) SetWriteDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}

func (w *deadlineRecordingWriter) FlushError() error { return w.flushErr }

// Stream write deadlines are set through http.ResponseController, which must reach the
// connection through this wrapper.
func TestRequestLoggerMiddlewareExposesWriteDeadlineAndFlushError(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	inner := &deadlineRecordingWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: errors.New("gone")}
	deadline := time.Now().Add(time.Minute)
	var setErr, flushErr error
	handler := RequestLoggerMiddleware(mockLog.Loggers)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		setErr = rc.SetWriteDeadline(deadline)
		flushErr = rc.Flush()
	}))
	req, _ := http.NewRequest("GET", "/url", nil)
	handler.ServeHTTP(inner, req)

	require.NoError(t, setErr)
	assert.Equal(t, []time.Time{deadline}, inner.deadlines)
	assert.Equal(t, inner.flushErr, flushErr)
}
