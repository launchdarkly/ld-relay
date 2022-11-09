package sharedtest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/eventsource"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/require"
)

// BuildRequest is a simple shortcut for creating a request that may or may not have a body.
func BuildRequest(method, url string, body []byte, headers http.Header) *http.Request {
	var bodyBuffer io.Reader
	if body != nil {
		bodyBuffer = bytes.NewBuffer(body)
	}
	r, err := http.NewRequest(method, url, bodyBuffer)
	if err != nil {
		panic(err)
	}
	if headers != nil {
		r.Header = headers
	}
	return r
}

// BuildRequestWithAuth creates a GET request with an Authorization header.
func BuildRequestWithAuth(method, url string, authKey config.SDKCredential, body []byte) *http.Request {
	h := make(http.Header)
	if authKey != nil {
		h.Add("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	return BuildRequest(method, url, body, h)
}

// AddQueryParam is a shortcut for concatenating a query string to a URL that may or may not have one.
func AddQueryParam(url, query string) string {
	if strings.Contains(url, "?") {
		return url + "&" + query
	}
	return url + "?" + query
}

// DoRequest is a shortcut for executing an endpoint handler against a request and getting the response.
func DoRequest(req *http.Request, handler http.Handler) (*http.Response, []byte) {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	result := w.Result()
	var body []byte
	if result.Body != nil {
		body, _ = io.ReadAll(result.Body)
		_ = result.Body.Close()
	}

	return result, body
}

// ExpectStreamEvent is a shortcut for reading from an SSE stream with a timeout.
func ExpectStreamEvent(t *testing.T, stream *eventsource.Stream, timeout time.Duration) eventsource.Event {
	return helpers.RequireValue(t, stream.Events, timeout, "timed out waiting for stream event")
}

// ExpectNoStreamEvent causes a test failure if an event is seen on an SSE stream.
func ExpectNoStreamEvent(t *testing.T, stream *eventsource.Stream, timeout time.Duration) {
	if !helpers.AssertNoMoreValues(t, stream.Events, timeout, "received unexpected stream event") {
		t.FailNow()
	}
}

// CallHandlerAndAwaitStatus calls an HTTP handler directly with a request and then blocks
// until the handler has started a response, returning the response status (and cancelling
// the request). We use this when we don't need to wait for a complete response (or when there's
// no such thing as a complete response, as in the case of streaming endpoints). It raises a
// fatal test failure if the timeout elapses before receiving a status.
func CallHandlerAndAwaitStatus(t *testing.T, handler http.Handler, req *http.Request, timeout time.Duration) int {
	var s simpleResponseSink
	go handler.ServeHTTP(&s, req)
	require.True(t, s.awaitResponseStarted(timeout), "timed out waiting for HTTP handler to start response")
	return s.getStatus()
}

// simpleResponseSink is used by CallHandlerAndAwaitStatus instead of httptest.ResponseRecorder
// because ResponseRecorder requires you to wait for a complete response.
type simpleResponseSink struct {
	status    int
	header    http.Header
	body      []byte
	started   bool
	startedCh chan struct{}
	lock      sync.Mutex
}

func (s *simpleResponseSink) awaitResponseStarted(timeout time.Duration) bool {
	s.lock.Lock()
	if s.started {
		s.lock.Unlock()
		return true
	}
	if s.startedCh == nil {
		s.startedCh = make(chan struct{}, 1)
	}
	ch := s.startedCh
	s.lock.Unlock()
	_, ok, _ := helpers.TryReceive(ch, timeout)
	return ok
}

func (s *simpleResponseSink) getStatus() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.status
}

func (s *simpleResponseSink) Header() http.Header {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *simpleResponseSink) Write(data []byte) (int, error) {
	s.start()
	s.lock.Lock()
	defer s.lock.Unlock()
	s.body = append(s.body, data...)
	return len(data), nil
}

func (s *simpleResponseSink) WriteHeader(status int) {
	s.lock.Lock()
	s.status = status
	s.lock.Unlock()
	s.start()
}

func (s *simpleResponseSink) Flush() {}

func (s *simpleResponseSink) start() {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.started {
		return
	}
	if s.status == 0 {
		s.status = 200
	}
	s.started = true
	if s.startedCh == nil {
		s.startedCh = make(chan struct{}, 1)
	}
	s.startedCh <- struct{}{}
}
