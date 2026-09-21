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

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
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

func TestRecoversAfterUnrecoverableHTTPError(t *testing.T) {
	// A rejected key no longer stops the stream. An operator can make the key valid again
	// without Relay knowing, so the stream keeps trying and recovers on its own, which
	// previously took a process restart.
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			initialEvent := makeEnvPutEvent(testEnv1)
			streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
			defer stream.Close()
			handler := httphelpers.SequentialHandler(
				httphelpers.HandlerWithStatus(status), // first request is rejected
				streamHandler,                         // the retry succeeds
			)
			streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
				// Shorten the extended delay so the retry happens within the test.
				p.streamManager.extendedRetryDelay = time.Millisecond
				p.startStream()

				<-p.requestsCh // the rejected request
				<-p.requestsCh // the retry

				p.requireMessage() // the environment from the recovered stream
				p.requireReceivedAllMessage()

				assert.True(t, p.mockLog.HasMessage(slog.LevelError, "will keep retrying"))
				assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "engaging extended backoff"))
			})
		})
	}
}

func TestServesTheCachedConfigurationWhileTheKeyIsRejected(t *testing.T) {
	// A rejected key leaves Relay running, so a cached configuration is worth having: Relay
	// serves those environments while it keeps trying the key. Before this change the process
	// exited and threw the cache away.
	handler := httphelpers.HandlerWithStatus(401)
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	cache := cacheWithContent{content: &PutContent{
		Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{testEnv1.EnvID: testEnv1},
	}}

	streamManagerTestWithCache(t, handler, stream, cache, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = time.Millisecond

		readyCh := p.streamManager.Start()

		// The cached environment reaches the handler, which is what makes Relay serviceable.
		p.requireMessage()
		p.requireReceivedAllMessage()

		if !helpers.AssertNoMoreValues(t, readyCh, time.Second, "Relay reported a failure") {
			t.FailNow()
		}
		assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "loaded from persistent cache"))
	})
}

func TestKeepsRunningWithNoConfigurationAtAll(t *testing.T) {
	// The case that used to exit the process. With a rejected key and nothing cached, Relay has
	// nothing to serve, and it still keeps running and retrying rather than reporting a failure.
	// It answers 503 until the key becomes valid, which is what it already did for every other
	// failure to reach LaunchDarkly.
	handler := httphelpers.HandlerWithStatus(401)
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = time.Millisecond

		readyCh := p.streamManager.Start()

		if !helpers.AssertNoMoreValues(t, readyCh, 500*time.Millisecond,
			"Relay reported a failure on a rejected key") {
			t.FailNow()
		}
		assert.True(t, p.mockLog.HasMessage(slog.LevelError, "will keep retrying"))
	})
}
