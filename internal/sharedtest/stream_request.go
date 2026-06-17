package sharedtest

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"

	"github.com/stretchr/testify/assert"
)

// StreamRecorder is an extension of ResponseRecorder to handle streaming content.
type StreamRecorder struct {
	*bufio.Writer
	*httptest.ResponseRecorder
}

func (r StreamRecorder) Write(data []byte) (int, error) {
	return r.Writer.Write(data)
}

func (r StreamRecorder) Flush() {
	_ = r.Writer.Flush()
}

func NewStreamRecorder() (StreamRecorder, io.Reader) {
	reader, writer := io.Pipe()
	recorder := httptest.NewRecorder()
	return StreamRecorder{
		ResponseRecorder: recorder,
		Writer:           bufio.NewWriter(writer),
	}, reader
}

// WithStreamRequest makes a request that should receive an SSE stream, and calls the given code
// with a channel that will read from that stream. A nil value is pushed to the channel when the
// stream closes or encounters an error.
func WithStreamRequest(
	t *testing.T,
	req *http.Request,
	handler http.Handler,
	action func(<-chan eventsource.Event),
) *http.Response {
	w, bodyReader := NewStreamRecorder()
	wg := sync.WaitGroup{}
	wg.Add(1)
	eventCh := make(chan eventsource.Event, 10)

	ctx, cancelRequest := context.WithCancel(context.Background())
	reqWithContext := req.WithContext(ctx)

	go func() {
		handler.ServeHTTP(w, reqWithContext)
		assert.Equal(t, http.StatusOK, w.Code)
		AssertStreamingContentType(t, w.Header())
		eventCh <- nil
		wg.Done()
	}()
	dec := eventsource.NewDecoder(bodyReader)
	go func() {
		gotEvent := false
		for {
			event, err := dec.Decode()
			if err == nil {
				eventCh <- event
				gotEvent = true
			} else {
				if !gotEvent {
					assert.NoError(t, err)
				}
				eventCh <- nil
				return
			}
		}
	}()
	action(eventCh)
	cancelRequest()
	wg.Wait()
	return w.Result()
}

// AwaitEventOfType reads from an event channel (such as the one passed to the action by
// WithStreamRequest) until it receives an event whose Event() type matches eventType, skipping any
// events of a different type. It calls t.Fatal if the stream closes (a nil value is received) or
// the timeout elapses before such an event arrives.
func AwaitEventOfType(t *testing.T, eventCh <-chan eventsource.Event, eventType string, timeout time.Duration) eventsource.Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case e := <-eventCh:
			if e == nil {
				t.Fatalf("stream closed before receiving event of type %q", eventType)
				return nil
			}
			if e.Event() == eventType {
				return e
			}
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for event of type %q", timeout, eventType)
			return nil
		}
	}
}

func WithStreamRequestLines(
	t *testing.T,
	req *http.Request,
	handler http.Handler,
	action func(<-chan string),
) *http.Response {
	w, bodyReader := NewStreamRecorder()
	wg := sync.WaitGroup{}
	wg.Add(1)
	linesCh := make(chan string, 10)

	ctx, cancelRequest := context.WithCancel(context.Background())
	reqWithContext := req.WithContext(ctx)

	go func() {
		handler.ServeHTTP(w, reqWithContext)
		linesCh <- ""
		assert.Equal(t, http.StatusOK, w.Code)
		AssertStreamingContentType(t, w.Header())
		wg.Done()
	}()
	r := bufio.NewReader(bodyReader)
	go func() {
		for {
			line, err := r.ReadString('\n')
			linesCh <- line
			if err != nil {
				return
			}
		}
	}()
	action(linesCh)
	cancelRequest()
	wg.Wait()
	return w.Result()
}
