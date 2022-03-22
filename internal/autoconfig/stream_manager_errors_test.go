package autoconfig

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
)

func eventShouldCauseStreamRestart(t *testing.T, event httphelpers.SSEEvent) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh
		p.stream.Enqueue(event)
		select {
		case <-p.messageHandler.received:
			require.Fail(t, "received unexpected message")
		case <-p.requestsCh: // got expected stream restart
			p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed JSON")
		case <-time.After(time.Second):
			require.Fail(t, "timed out waiting for stream restart")
		}
	})
}

func TestMalformedJSONInEventCausesStreamRestart(t *testing.T) {
	t.Run("put", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: PutEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("patch", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("delete", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})
}

func TestWellFormedJSONThatIsNotWellFormedEventDataCausesStreamRestart(t *testing.T) {
	t.Run("put", func(t *testing.T) {
		json := `{"path": "/", "data": {"environments": {"envid1": 999}}}`
		event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("patch", func(t *testing.T) {
		json := `{"path": "/environments/envid1","data": 999}`
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("delete", func(t *testing.T) {
		json := `{"path": 999}`
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: json}
		eventShouldCauseStreamRestart(t, event)
	})
}

func errorShouldCauseReconnect(t *testing.T, errorProducingHandler http.Handler, expectedWarning string) {
	initialEvent := makePutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		errorProducingHandler, // first request will get this
		streamHandler,         // request after reconnect will get this
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh // first request
		select {
		case <-p.requestsCh: // got expected stream restart
			p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, expectedWarning)
		case <-time.After(time.Second):
			require.Fail(t, "timed out waiting for stream restart")
		}
		msg := p.requireMessage()
		assert.NotNil(t, msg.add)
		p.requireReceivedAllMessage()
	})
}

func TestReconnectAfterRecoverableHTTPError(t *testing.T) {
	for _, status := range []int{400, 500, 503} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			errorShouldCauseReconnect(t, httphelpers.HandlerWithStatus(status), fmt.Sprintf("HTTP error %d", status))
		})
	}
}

func TestReconnectAfterNetworkError(t *testing.T) {
	errorShouldCauseReconnect(t, httphelpers.BrokenConnectionHandler(), "Unexpected error")
}

func TestNoReconnectAfterUnrecoverableHTTPError(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			initialEvent := makePutEvent(testEnv1)
			streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
			defer stream.Close()
			errorProducingHandler := httphelpers.HandlerWithStatus(status)
			handler := httphelpers.SequentialHandler(
				errorProducingHandler, // first request will get this
				streamHandler,         // request after reconnect will get this
			)
			streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
				p.startStream()
				<-p.requestsCh // first request
				select {
				case <-p.requestsCh: // got expected stream restart
					require.Fail(t, "got unexpected stream restart")
				case <-p.messageHandler.received:
					require.Fail(t, "got unexpected event")
				case <-time.After(time.Millisecond * 200):
					p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "Invalid auto-configuration key")
				}
			})
		})
	}
}
