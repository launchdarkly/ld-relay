package autoconfig

import (
	"runtime"
	"testing"
	"time"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
)

// TestStreamManagerCloseDuringStreamingDoesNotHang is a regression test for a hang in which
// StreamManager.Close blocked forever (and so did Relay.Close, which calls it). consumeStream's
// deferred drain of stream.Events could never complete because the eventsource library can deadlock
// internally when Close races with an in-flight event, leaving stream.Events unclosed. The drain is
// now backgrounded so consumeStream returns and s.done closes regardless. See consumeStream.
//
// The race is timing-dependent, so this floods the stream with events and then closes, repeated many
// times to make the close-vs-event-delivery interleaving overwhelmingly likely. On regression the
// process hangs until the timeout below fires, printing all goroutines to pinpoint the block.
func TestStreamManagerCloseDuringStreamingDoesNotHang(t *testing.T) {
	heartbeat := httphelpers.SSEEvent{Event: "heartbeat", Data: "{}"}

	for i := 0; i < 60; i++ {
		streamManagerTest(t, &emptyPutMessage, func(p streamManagerTestParams) {
			p.startStream()
			p.requireReceivedAllMessage()

			// Flood the stream so eventsource is very likely mid-delivery of a decoded event when we
			// Close, exercising the close-vs-event-delivery race. These are unknown events, so the
			// message handler is never called and consumeStream loops without blocking on it.
			go func() {
				for j := 0; j < 2000; j++ {
					p.stream.Enqueue(heartbeat)
				}
			}()

			time.Sleep(2 * time.Millisecond)

			done := make(chan struct{})
			go func() {
				p.streamManager.Close()
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Fatalf("StreamManager.Close() hung on iteration %d (regression)\n%s", i, buf[:n])
			}
		})
	}
}
