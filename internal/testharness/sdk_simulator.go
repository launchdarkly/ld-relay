package testharness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/eventsource"
)

// SDKSimulator simulates a downstream SDK client connecting to relay's server-side SSE
// stream (/all endpoint). It establishes a real HTTP connection against a running
// httptest.Server and exposes the received events for assertion.
type SDKSimulator struct {
	eventCh chan eventsource.Event
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewSDKSimulator creates a simulator that connects to server's /all endpoint using
// sdkKey as the Authorization credential. The connection is established in a background
// goroutine; call AwaitEvent or AwaitEventOfType to wait for events.
//
// Cleanup is registered with t.Cleanup; call Close() explicitly only if you need early
// teardown.
func NewSDKSimulator(t testing.TB, server *httptest.Server, sdkKey config.SDKKey) *SDKSimulator {
	ctx, cancel := context.WithCancel(context.Background())
	s := &SDKSimulator{
		eventCh: make(chan eventsource.Event, 16),
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/all", nil)
	if err != nil {
		cancel()
		t.Fatalf("SDKSimulator: failed to build request: %v", err)
	}
	req.Header.Set("Authorization", sdkKey.GetAuthorizationHeaderValue())
	req.Header.Set("Accept", "text/event-stream")

	go func() {
		defer close(s.done)
		defer close(s.eventCh)

		resp, err := server.Client().Do(req)
		if err != nil {
			// Context cancellation is not an error from the test's perspective.
			if ctx.Err() == nil {
				select {
				case s.eventCh <- &simulatorErrorEvent{err: err}:
				default:
				}
			}
			return
		}
		defer resp.Body.Close()

		dec := eventsource.NewDecoder(resp.Body)
		for {
			event, err := dec.Decode()
			if err != nil {
				return
			}
			select {
			case s.eventCh <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	t.Cleanup(s.Close)
	return s
}

// AwaitEvent returns the next event from the SSE stream, blocking until one arrives or
// the timeout expires. Returns nil on timeout or stream close.
func (s *SDKSimulator) AwaitEvent(timeout time.Duration) eventsource.Event {
	select {
	case e, ok := <-s.eventCh:
		if !ok {
			return nil
		}
		return e
	case <-time.After(timeout):
		return nil
	}
}

// AwaitEventOfType waits for the next SSE event whose Event field matches eventType,
// skipping any events with a different type. It calls t.Fatal if the timeout expires
// before such an event arrives.
func (s *SDKSimulator) AwaitEventOfType(t testing.TB, eventType string, timeout time.Duration) eventsource.Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case e, ok := <-s.eventCh:
			if !ok {
				t.Fatalf("SDKSimulator: stream closed before receiving event of type %q", eventType)
				return nil
			}
			if e.Event() == eventType {
				return e
			}
		case <-deadline.C:
			t.Fatalf("SDKSimulator: timed out after %s waiting for event of type %q", timeout, eventType)
			return nil
		}
	}
}

// Close disconnects the simulator.
func (s *SDKSimulator) Close() {
	s.cancel()
	<-s.done
}

// simulatorErrorEvent is a sentinel event used to propagate connection errors through
// the event channel. It implements eventsource.Event.
type simulatorErrorEvent struct {
	err error
}

func (e *simulatorErrorEvent) Event() string { return "error" }
func (e *simulatorErrorEvent) Data() string  { return fmt.Sprintf("connection error: %v", e.err) }
func (e *simulatorErrorEvent) Id() string    { return "" }
