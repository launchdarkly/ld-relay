package autoconfig

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
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

// A credential payload that is valid JSON and a structurally valid event, but whose credential set
// cannot be built (here: an undefined anchor SDK key), must be caught at the parse boundary: the
// previous state is preserved (no AddEnvironment/UpdateEnvironment dispatched) and the stream is
// restarted so the backend resends a fresh put (design §9). This is verified for both patch and put,
// since both paths run the validation before the version is recorded.
func TestMalformedCredentialPayloadCausesStreamRestart(t *testing.T) {
	malformedEnv := testEnv1
	malformedEnv.SDKKey = envfactory.SDKKeyRep{Value: config.SDKKey("")} // undefined anchor

	t.Run("patch", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			p.stream.Enqueue(makePatchEnvEvent(malformedEnv))
			select {
			case m := <-p.messageHandler.received:
				require.Failf(t, "unexpected message",
					"must not dispatch for a malformed payload, got %s", m)
			case <-p.requestsCh: // reconnect request == stream restart
				p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed credential payload")
			case <-time.After(time.Second):
				require.Fail(t, "timed out waiting for stream restart")
			}
		})
	})

	t.Run("put", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			p.stream.Enqueue(makeEnvPutEvent(malformedEnv))
			// The malformed env is skipped (no add/update); a put still reports ReceivedAllEnvironments,
			// which we tolerate. We require that the stream restarts and that no add/update is dispatched.
			deadline := time.After(2 * time.Second)
			for {
				select {
				case m := <-p.messageHandler.received:
					if m.add != nil || m.update != nil {
						require.Failf(t, "unexpected message",
							"must not dispatch add/update for a malformed payload, got %s", m)
					}
				case <-p.requestsCh: // reconnect request == stream restart
					p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed credential payload")
					return
				case <-deadline:
					require.Fail(t, "timed out waiting for stream restart")
				}
			}
		})
	})
}

// A put carrying a malformed credential payload must not be written to the persistent cache: doing so
// would re-surface the rejected environment (skipped again) on the next cache load. The malformed env
// is skipped in memory and the stream reconnects for a fresh put, which is what refreshes the cache.
func TestMalformedCredentialPayloadInPutIsNotCached(t *testing.T) {
	malformedEnv := testEnv1
	malformedEnv.SDKKey = envfactory.SDKKeyRep{Value: config.SDKKey("")} // undefined anchor

	t.Run("malformed put is not persisted", func(t *testing.T) {
		cache := &recordingCache{}
		streamManagerTestWithCache(t, nil, cache, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			p.stream.Enqueue(makeEnvPutEvent(malformedEnv))
			_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")
			assert.Equal(t, 0, cache.setAllCount(), "a malformed put must not be written to the cache")
		})
	})

	t.Run("valid put is persisted", func(t *testing.T) {
		cache := &recordingCache{}
		streamManagerTestWithCache(t, nil, cache, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			p.stream.Enqueue(makeEnvPutEvent(testEnv1))
			assert.Eventually(t, func() bool { return cache.setAllCount() == 1 }, time.Second, 10*time.Millisecond,
				"a valid put must be written to the cache")
		})
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

func errorShouldCauseReconnect(t *testing.T, errorProducingHandler http.Handler, expectedWarning string) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		errorProducingHandler, // first request will get this
		streamHandler,         // request after reconnect will get this
	)
	streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh // first request
		_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")
		p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, expectedWarning)
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
			initialEvent := makeEnvPutEvent(testEnv1)
			streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
			defer stream.Close()
			errorProducingHandler := httphelpers.HandlerWithStatus(status)
			handler := httphelpers.SequentialHandler(
				errorProducingHandler, // first request will get this
				streamHandler,         // request after reconnect will get this
			)
			streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
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
