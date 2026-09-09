package autoconfig

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusPollInterval is how often the status assertions re-read the state. The transitions happen on
// the eventsource and stream-consuming goroutines, so a test cannot observe them synchronously.
const (
	statusPollTimeout  = 2 * time.Second
	statusPollInterval = 10 * time.Millisecond
)

// requireStatusEventually waits for the stream status to satisfy the condition.
func requireStatusEventually(t *testing.T, p streamManagerTestParams, msg string, cond func(StreamStatus) bool) StreamStatus {
	t.Helper()
	var last StreamStatus
	require.Eventuallyf(t, func() bool {
		last = p.streamManager.Status()
		return cond(last)
	}, statusPollTimeout, statusPollInterval, "%s; last status was %+v", msg, &last)
	return last
}

// cacheWithContent is a Cache whose read returns fixed content, so a test can exercise the path
// where Relay serves from the cache before the stream has provided anything.
type cacheWithContent struct {
	noopTestCache
	content *PutContent
}

func (c cacheWithContent) GetAll(context.Context) (*PutContent, error) {
	return c.content, nil
}

func TestStreamStatusIsInitializingBeforeConnection(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		// The stream is deliberately not started.
		status := p.streamManager.Status()
		assert.Equal(t, interfaces.DataSourceStateInitializing, status.State)
		assert.False(t, status.StateSince.IsZero())
		assert.Empty(t, status.LastError.Kind)
	})
}

func TestStreamStatusIsValidAfterPut(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()

		status := requireStatusEventually(t, p, "expected VALID after a put", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})
		assert.Empty(t, status.LastError.Kind)
	})
}

func TestStreamStatusIsValidAfterPatch(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()

		updated := testEnv1
		updated.Version++
		updated.EnvName = "renamed"
		p.stream.Enqueue(makePatchEnvEvent(updated))
		p.requireMessage()

		requireStatusEventually(t, p, "expected VALID after a patch", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})
	})
}

// A first connection that has never succeeded must keep reporting INITIALIZING, so the document
// never implies the stream was once working. The error is still recorded.
func TestStreamStatusStaysInitializingWhenFirstConnectionFails(t *testing.T) {
	streamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		httphelpers.HandlerWithStatus(503),
		httphelpers.HandlerWithStatus(503),
		streamHandler,
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		// Start without waiting for readiness: the point is the state while no connection has worked.
		_ = p.streamManager.Start()

		status := requireStatusEventually(t, p, "expected INITIALIZING with an error", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateInitializing && s.LastError.Kind != ""
		})
		assert.Equal(t, interfaces.DataSourceErrorKindErrorResponse, status.LastError.Kind)
		assert.Equal(t, 503, status.LastError.StatusCode)
	})
}

func TestStreamStatusIsInterruptedAfterHTTPError(t *testing.T) {
	for _, status := range []int{400, 500, 503} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			initialEvent := makeEnvPutEvent(testEnv1)
			streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
			defer stream.Close()
			handler := httphelpers.SequentialHandler(
				streamHandler, // the first connection works, so the state reaches VALID
				httphelpers.HandlerWithStatus(status),
				httphelpers.HandlerWithStatus(status),
			)
			streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
				p.startStream()
				p.requireMessage()
				p.requireReceivedAllMessage()
				requireStatusEventually(t, p, "expected VALID before the failure", func(s StreamStatus) bool {
					return s.State == interfaces.DataSourceStateValid
				})

				stream.EndAll() // drop the connection, so the retries meet the error handler

				got := requireStatusEventually(t, p, "expected INTERRUPTED with an HTTP error",
					func(s StreamStatus) bool {
						return s.State == interfaces.DataSourceStateInterrupted &&
							s.LastError.Kind == interfaces.DataSourceErrorKindErrorResponse
					})
				assert.Equal(t, status, got.LastError.StatusCode)
			})
		})
	}
}

func TestStreamStatusIsInterruptedAfterNetworkError(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		streamHandler,
		httphelpers.BrokenConnectionHandler(),
		httphelpers.BrokenConnectionHandler(),
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()
		requireStatusEventually(t, p, "expected VALID before the failure", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})

		stream.EndAll()

		requireStatusEventually(t, p, "expected INTERRUPTED with a network error", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateInterrupted &&
				s.LastError.Kind == interfaces.DataSourceErrorKindNetworkError
		})
	})
}

// After an interruption, the next event received restores VALID, and the error stays readable.
func TestStreamStatusRecoversToValidAfterInterruption(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		streamHandler,
		httphelpers.HandlerWithStatus(503),
		streamHandler,
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()

		stream.EndAll()

		// Wait for the failure to be recorded before looking for the recovery. The harness retries
		// after a millisecond, so the INTERRUPTED window itself is not reliably observable, and a
		// bare wait for VALID would pass on the state the stream was already in.
		// TestStreamStatusIsInterruptedAfterHTTPError covers that state with a server that keeps
		// failing. Which error survives here depends on whether the dropped connection or the 503
		// that follows it lands last, so only its presence is asserted.
		requireStatusEventually(t, p, "expected an error to be recorded", func(s StreamStatus) bool {
			return s.LastError.Kind != ""
		})

		status := requireStatusEventually(t, p, "expected VALID again", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})
		assert.NotEmpty(t, status.LastError.Kind, "the last error should remain readable after recovery")
	})
}

func TestStreamStatusRecordsInvalidDataError(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()

		p.stream.Enqueue(httphelpers.SSEEvent{Event: PutEvent, Data: malformedJSON})

		requireStatusEventually(t, p, "expected an invalid-data error", func(s StreamStatus) bool {
			return s.LastError.Kind == interfaces.DataSourceErrorKindInvalidData
		})

		// Malformed data restarts the stream, and this test's server replays its put on the new
		// connection, so the state returns to VALID with the error still readable.
		status := requireStatusEventually(t, p, "expected VALID after the restart", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})
		assert.Equal(t, interfaces.DataSourceErrorKindInvalidData, status.LastError.Kind)
	})
}

func TestStreamStatusIsOffAfterUnrecoverableHTTPError(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			streamHandler, stream := httphelpers.SSEHandler(nil)
			defer stream.Close()
			handler := httphelpers.SequentialHandler(
				httphelpers.HandlerWithStatus(status),
				streamHandler,
			)
			streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
				readyCh := p.streamManager.Start()
				err := helpers.RequireValue(t, readyCh, time.Second, "timed out waiting for stream failure")
				require.Error(t, err)

				got := requireStatusEventually(t, p, "expected OFF", func(s StreamStatus) bool {
					return s.State == interfaces.DataSourceStateOff
				})
				assert.Equal(t, interfaces.DataSourceErrorKindErrorResponse, got.LastError.Kind)
				assert.Equal(t, status, got.LastError.StatusCode)
			})
		})
	}
}

func TestStreamStatusIsOffAfterClose(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()

		p.streamManager.Close()
		assert.Equal(t, interfaces.DataSourceStateOff, p.streamManager.Status().State)
	})
}

// Cached content lets Relay serve environments, but it is not a stream connection: the state must
// stay INITIALIZING until the stream itself provides data. This is what tells a caller that a Relay
// reporting a healthy status has never reached LaunchDarkly.
func TestStreamStatusIgnoresCachedContent(t *testing.T) {
	cache := cacheWithContent{content: &PutContent{
		Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{testEnv1.EnvID: testEnv1},
	}}
	streamHandler, stream := httphelpers.SSEHandler(nil) // connects, but sends no events
	defer stream.Close()
	streamManagerTestWithCache(t, streamHandler, stream, cache, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage() // the cached environment was applied
		p.requireReceivedAllMessage()

		status := p.streamManager.Status()
		assert.Equal(t, interfaces.DataSourceStateInitializing, status.State)
		assert.Empty(t, status.LastError.Kind)
	})
}

// The state rules are asserted directly here as well as through a live stream. Close waits for the
// stream goroutine to exit, so a test driving a real stream cannot reach the interleaving the
// terminal-OFF rule exists for, and the timing-dependent tests above cannot show that a repeated
// failure leaves stateSince alone.
func TestStreamStatusRules(t *testing.T) {
	newManager := func() *StreamManager {
		return &StreamManager{status: StreamStatus{
			State:      interfaces.DataSourceStateInitializing,
			StateSince: time.Now(),
		}}
	}

	t.Run("OFF is terminal", func(t *testing.T) {
		s := newManager()
		s.updateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		s.updateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{})
		off := s.Status()

		s.updateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		s.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindNetworkError,
		})

		assert.Equal(t, off, s.Status(), "no state change is accepted after OFF")
	})

	t.Run("stateSince only moves when the state changes", func(t *testing.T) {
		s := newManager()
		s.updateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		s.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind:       interfaces.DataSourceErrorKindErrorResponse,
			StatusCode: 500,
		})
		first := s.Status()

		s.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind:       interfaces.DataSourceErrorKindErrorResponse,
			StatusCode: 503,
		})
		second := s.Status()

		assert.Equal(t, first.StateSince, second.StateSince,
			"a repeated failure must not restate how long the stream has been broken")
		assert.Equal(t, 503, second.LastError.StatusCode, "the newer error is still recorded")
	})

	t.Run("an empty state is ignored", func(t *testing.T) {
		s := newManager()
		s.updateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		before := s.Status()

		s.updateStatus("", interfaces.DataSourceErrorInfo{})

		assert.Equal(t, before, s.Status())
	})

	t.Run("a success is discarded if a failure was recorded first", func(t *testing.T) {
		s := newManager()
		s.updateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
		generation := s.failureGeneration()

		s.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindNetworkError,
		})
		s.markValid(generation)

		assert.Equal(t, interfaces.DataSourceStateInterrupted, s.Status().State)
		assert.Equal(t, interfaces.DataSourceStateValid, func() interfaces.DataSourceState {
			s.markValid(s.failureGeneration())
			return s.Status().State
		}(), "a success read after the failure is accepted")
	})
}

// blockingMessageHandler blocks inside one dispatch call. That is where the real Relay creates
// environments, starts SDK clients, and writes the auto-config cache, so it stands in for any
// dispatch that takes longer than the stream takes to fail.
type blockingMessageHandler struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func newBlockingMessageHandler() *blockingMessageHandler {
	return &blockingMessageHandler{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (h *blockingMessageHandler) arm() {
	h.mu.Lock()
	h.armed = true
	h.mu.Unlock()
}

func (h *blockingMessageHandler) maybeBlock() {
	h.mu.Lock()
	armed := h.armed
	h.armed = false
	h.mu.Unlock()
	if !armed {
		return
	}
	h.entered <- struct{}{}
	<-h.release
}

func (h *blockingMessageHandler) AddEnvironment(envfactory.EnvironmentParams)    { h.maybeBlock() }
func (h *blockingMessageHandler) UpdateEnvironment(envfactory.EnvironmentParams) { h.maybeBlock() }
func (h *blockingMessageHandler) DeleteEnvironment(config.EnvironmentID)         { h.maybeBlock() }
func (h *blockingMessageHandler) ReceivedAllEnvironments()                       {}
func (h *blockingMessageHandler) AddFilter(envfactory.FilterParams)              {}
func (h *blockingMessageHandler) DeleteFilter(config.FilterID)                   {}

// newStreamManagerForHandlers builds a StreamManager against an arbitrary message handler, which the
// shared harness does not allow.
func newStreamManagerForHandlers(t *testing.T, server *httptest.Server, handler MessageHandler) *StreamManager {
	t.Helper()
	logger, _ := logtest.NewMockLogger()
	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", logger)
	require.NoError(t, err)
	return NewStreamManager(
		testConfigKey,
		mustParseURL(t, server.URL),
		handler,
		httpConfig,
		time.Millisecond,
		rpacProtocolVersion,
		logger,
		noopTestCache{},
	)
}

func waitForStreamState(t *testing.T, sm *StreamManager, want interfaces.DataSourceState, msg string) StreamStatus {
	t.Helper()
	var last StreamStatus
	require.Eventuallyf(t, func() bool {
		last = sm.Status()
		return last.State == want
	}, statusPollTimeout, 5*time.Millisecond, "%s; last status was %+v", msg, &last)
	return last
}

// An event that was still being handled when the connection failed is not evidence that the
// connection works. Without the generation check the recorded failure is overwritten, and if the
// reconnect never answers, nothing reports on the stream again: it claims VALID while dead.
func TestStreamStatusKeepsAFailureThatInterruptedAnEvent(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()

	// The reconnect hangs without responding and without failing, so the error handler is never
	// called again. This is an ordinary TCP blackhole: a dropped route, a wedged proxy, a SYN that
	// gets no answer.
	hang := make(chan struct{})
	var requests int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			streamHandler.ServeHTTP(w, r)
			return
		}
		<-hang
	})

	messageHandler := newBlockingMessageHandler()

	httphelpers.WithServer(autoConfigEndpointHandler(handler), func(server *httptest.Server) {
		sm := newStreamManagerForHandlers(t, server, messageHandler)
		// The hung request has to be released before Close: consumeStream's deferred drain of
		// stream.Events does not return until eventsource closes that channel, which it cannot do
		// until the in-flight reconnect returns.
		var releaseOnce sync.Once
		defer func() {
			releaseOnce.Do(func() { close(hang) })
			sm.Close()
		}()

		readyCh := sm.Start()
		require.NoError(t, helpers.RequireValue(t, readyCh, time.Second, "stream never became ready"))
		waitForStreamState(t, sm, interfaces.DataSourceStateValid, "expected VALID after the initial put")

		// A patch arrives and its dispatch blocks, the way a real environment update does.
		messageHandler.arm()
		updated := testEnv1
		updated.Version++
		updated.EnvName = "renamed"
		stream.Enqueue(makePatchEnvEvent(updated))
		<-messageHandler.entered

		// The connection fails while that dispatch is still running.
		stream.EndAll()
		interrupted := waitForStreamState(t, sm, interfaces.DataSourceStateInterrupted,
			"expected INTERRUPTED once the connection dropped")
		require.NotEmpty(t, interrupted.LastError.Kind)

		// The dispatch finishes, and its success must not be recorded. Nothing else will ever report
		// on this connection, so whatever is recorded here is what a probe sees indefinitely.
		close(messageHandler.release)
		assert.Never(t, func() bool {
			return sm.Status().State == interfaces.DataSourceStateValid
		}, time.Second, 25*time.Millisecond,
			"a dead stream reported VALID: the interrupted event overwrote the recorded failure")
	})
}

// Any event the stream delivers proves the connection works, including one this version does not
// recognize. Requiring a recognized event would leave a healthy Relay reporting a stale failure,
// and eventsource discards the stream's heartbeat comments, so no other liveness signal exists.
func TestStreamStatusRecoversOnAnUnrecognizedEvent(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	firstHandler, firstStream := httphelpers.SSEHandler(&initialEvent)
	defer firstStream.Close()

	// Every connection after the first delivers only an event name this version does not know.
	futureEvent := httphelpers.SSEEvent{Event: "server-intent", Data: `{"payloads":[]}`}
	secondHandler, secondStream := httphelpers.SSEHandler(&futureEvent)
	defer secondStream.Close()

	var requests int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			firstHandler.ServeHTTP(w, r)
			return
		}
		secondHandler.ServeHTTP(w, r)
	})

	httphelpers.WithServer(autoConfigEndpointHandler(handler), func(server *httptest.Server) {
		sm := newStreamManagerForHandlers(t, server, newBlockingMessageHandler())
		defer sm.Close()

		readyCh := sm.Start()
		require.NoError(t, helpers.RequireValue(t, readyCh, time.Second, "stream never became ready"))
		waitForStreamState(t, sm, interfaces.DataSourceStateValid, "expected VALID after the initial put")

		// Drop the connection. The reconnect is healthy but speaks only the unknown event, and the
		// recorded failure must not outlive it. The INTERRUPTED window in between is not asserted:
		// the reconnect delivers its event too quickly for that state to be reliably observable.
		firstStream.EndAll()
		secondStream.Send(futureEvent)

		// Waiting for VALID alone would pass on the state the stream was already in, so this waits
		// for the pair: the drop recorded an error, and the unknown event that followed it restored
		// VALID. The INTERRUPTED window in between is not asserted, because the reconnect delivers
		// its event too quickly for that state to be reliably observable.
		var last StreamStatus
		require.Eventuallyf(t, func() bool {
			last = sm.Status()
			return last.LastError.Kind != "" && last.State == interfaces.DataSourceStateValid
		}, statusPollTimeout, 5*time.Millisecond,
			"a connected, event-delivering stream kept reporting a failure; last status was %+v", &last)
	})
}

// A key revoked mid-stream is the case where this status is the only signal. The error never reaches
// the channel Relay exits on, because signalReady has already fired, so Relay keeps serving with a
// configuration stream that will not be retried.
func TestStreamStatusIsOffAfterMidStreamKeyRevocation(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		streamHandler,
		httphelpers.HandlerWithStatus(401),
	)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		p.requireMessage()
		p.requireReceivedAllMessage()
		requireStatusEventually(t, p, "expected VALID before the revocation", func(s StreamStatus) bool {
			return s.State == interfaces.DataSourceStateValid
		})

		stream.EndAll()

		got := requireStatusEventually(t, p, "expected OFF after the key was rejected",
			func(s StreamStatus) bool {
				return s.State == interfaces.DataSourceStateOff
			})
		assert.Equal(t, interfaces.DataSourceErrorKindErrorResponse, got.LastError.Kind)
		assert.Equal(t, 401, got.LastError.StatusCode)
	})
}
