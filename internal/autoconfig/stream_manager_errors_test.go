package autoconfig

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
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
			assert.True(t, p.mockLog.HasMessage(slog.LevelError, "malformed JSON"))
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
		t.Run("without filters", func(t *testing.T) {
			json := `{"path": "/", "data": {"environments": {"envid1": 999}}}`
			event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
		t.Run("with filters", func(t *testing.T) {
			json := `{"path": "/", "data": {"environments": {"envid1": 999}, "filters": {"filter1":999}}}`
			event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
	})

	t.Run("patch", func(t *testing.T) {
		t.Run("environments", func(t *testing.T) {
			json := `{"path": "/environments/envid1","data": 999}`
			event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
		t.Run("filters", func(t *testing.T) {
			json := `{"path": "/filters/filterid1","data": 999}`
			event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
	})

	t.Run("delete", func(t *testing.T) {
		json := `{"path": 999}`
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: json}
		eventShouldCauseStreamRestart(t, event)
	})
}

// errorShouldCauseReconnect verifies that the given handler's error response causes the stream
// to reconnect and that a warning log entry with the expected message is emitted. If
// expectedStatusCode is non-zero, it also verifies that the entry has a "statusCode" attribute
// matching that value.
func errorShouldCauseReconnect(t *testing.T, errorProducingHandler http.Handler, expectedWarning string, expectedStatusCode int) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		errorProducingHandler, // first request will get this
		streamHandler,         // request after reconnect will get this
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh // first request
		_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")

		found := false
		for _, e := range p.mockLog.EntriesForLevel(slog.LevelWarn) {
			if !strings.Contains(e.Message, expectedWarning) {
				continue
			}
			if expectedStatusCode != 0 {
				assert.EqualValues(t, expectedStatusCode, e.Attrs["statusCode"], "statusCode attribute mismatch")
			}
			found = true
			break
		}
		assert.True(t, found, "expected warn-level log entry containing %q", expectedWarning)

		msg := p.requireMessage()
		assert.NotNil(t, msg.add)
		p.requireReceivedAllMessage()
	})
}

func TestReconnectAfterRecoverableHTTPError(t *testing.T) {
	for _, status := range []int{400, 500, 503} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			errorShouldCauseReconnect(t, httphelpers.HandlerWithStatus(status), "HTTP error", status)
		})
	}
}

func TestReconnectAfterNetworkError(t *testing.T) {
	errorShouldCauseReconnect(t, httphelpers.BrokenConnectionHandler(), "unexpected error", 0)
}

func TestNoReconnectAfterUnrecoverableHTTPError(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			initialEvent := makeEnvPutEvent(testEnv1)
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
					assert.True(t, p.mockLog.HasMessage(slog.LevelError, "invalid auto-configuration key"))
				}
			})
		})
	}
}
