package streams

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"

	"github.com/launchdarkly/eventsource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEvent struct {
	event string
	data  string
}

func (e testEvent) Event() string {
	return e.event
}

func (e testEvent) Data() string {
	return e.data
}

func (e testEvent) Id() string {
	return ""
}

func (e testEvent) Retry() int64 {
	return 0
}

func verifyServerProperties(t *testing.T, server *eventsource.Server, maxConnTime time.Duration) {
	require.NotNil(t, server)
	assert.False(t, server.Gzip)
	assert.True(t, server.AllowCORS)
	assert.True(t, server.ReplayAll)
	assert.Equal(t, maxConnTime, server.MaxConnTime)
}

func verifyHandlerGetsPublishedEvent(t *testing.T, sp StreamProvider, credential config.SDKCredential, key string, server *eventsource.Server) {
	handler := sp.Handler(credential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		expected := testEvent{event: "put", data: "data"}
		server.Publish([]string{key}, expected)

		select {
		case e := <-eventCh:
			require.NotNil(t, e)
			assert.Equal(t, expected.Event(), e.Event())
			assert.Equal(t, expected.Data(), e.Data())
		case <-time.After(time.Second):
			assert.Fail(t, "timed out waiting for event")
		}
	})
}

func expectEvent(t *testing.T, eventCh <-chan eventsource.Event, expected eventsource.Event) {
	select {
	case e := <-eventCh:
		require.NotNil(t, e)
		assert.Equal(t, expected.Event(), e.Event())
		assert.Equal(t, expected.Data(), e.Data())
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for event")
	}
}

func expectNoEvent(t *testing.T, eventCh <-chan eventsource.Event) {
	select {
	case e := <-eventCh:
		assert.Fail(t, "received unexpected event", "%+v", e)
	case <-time.After(time.Millisecond * 50):
		return
	}
}

func verifyHandlerInitialEvent(t *testing.T, sp StreamProvider, credential config.SDKCredential, expected eventsource.Event) {
	handler := sp.Handler(credential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		if expected == nil {
			expectNoEvent(t, eventCh)
		} else {
			expectEvent(t, eventCh, expected)
		}
	})
}

func verifyHandlerUpdateEvent(
	t *testing.T,
	sp StreamProvider,
	credential config.SDKCredential,
	expectedInitialEvent eventsource.Event,
	action func(),
	expectedUpdateEvent eventsource.Event,
) {
	handler := sp.Handler(credential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		expectEvent(t, eventCh, expectedInitialEvent)

		action()

		if expectedUpdateEvent == nil {
			expectNoEvent(t, eventCh)
		} else {
			expectEvent(t, eventCh, expectedUpdateEvent)
		}
	})
}

func verifyHandlerHeartbeat(
	t *testing.T,
	sp StreamProvider,
	esp EnvStreamProvider,
	credential config.SDKCredential,
) {
	handler := sp.Handler(credential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequestLines(t, req, handler, func(linesCh <-chan string) {
	ReadInitialEvent:
		for {
			select {
			case line := <-linesCh:
				if strings.HasPrefix(line, ":") {
					assert.Fail(t, "received comment too soon")
					return
				}
				if line == "\n" {
					break ReadInitialEvent
				}
			case <-time.After(time.Second):
				assert.Fail(t, "timed out waiting for initial event")
				return
			}
		}

		esp.SendHeartbeat()

		select {
		case line := <-linesCh:
			if !strings.HasPrefix(line, ":") {
				assert.Fail(t, "received unexpected non-comment data")
			}
		case <-time.After(time.Second):
			assert.Fail(t, "timed out waiting for heartbeat")
			return
		}
	})
}
