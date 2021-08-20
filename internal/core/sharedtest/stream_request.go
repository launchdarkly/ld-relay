package sharedtest

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
