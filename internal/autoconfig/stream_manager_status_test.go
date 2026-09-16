package autoconfig

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A working stream reports VALID with no error, and only once an event has actually been
// handled: connecting is not by itself evidence that the stream works.
//
// The stream starts with no events queued, so the state after connecting can be read without
// racing an event dispatch. Handing the harness an initial event instead would deliver it
// immediately, and the assertion below would depend on winning a race against the supervisor
// goroutine.
func TestStatusIsValidOnceAnEventIsHandled(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh

		assert.Equal(t, interfaces.DataSourceStateInitializing, p.streamManager.Status().State,
			"connecting alone must not report the stream as working")

		p.stream.Enqueue(makeEnvPutEvent(testEnv1))
		p.requireMessage()
		p.requireReceivedAllMessage()

		require.Eventually(t, func() bool {
			return p.streamManager.Status().State == interfaces.DataSourceStateValid
		}, time.Second, 10*time.Millisecond, "a handled event must report VALID")
		assert.Empty(t, p.streamManager.Status().LastError.Kind)
	})
}

// A recoverable HTTP failure records the status code, which the status resource reports. Before
// this, a rejected or failing stream appeared as a bare error kind with no code.
func TestStatusRecordsTheHTTPStatusCode(t *testing.T) {
	handler := httphelpers.SequentialHandler(
		httphelpers.HandlerWithStatus(503),
		httphelpers.HandlerWithStatus(503),
	)
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
		p.streamManager.Start()

		require.Eventually(t, func() bool {
			return p.streamManager.Status().LastError.Kind != ""
		}, 2*time.Second, 10*time.Millisecond, "expected the failure to be recorded")

		st := p.streamManager.Status()
		assert.Equal(t, interfaces.DataSourceErrorKindErrorResponse, st.LastError.Kind)
		assert.Equal(t, 503, st.LastError.StatusCode)

		// It has never connected, so an interruption keeps the initializing state rather than
		// claiming the stream was once working.
		assert.Equal(t, interfaces.DataSourceStateInitializing, st.State)
	})
}

// A rejected key is not terminal: the stream keeps retrying, so it records the failure without
// reporting the stream as finished, and it reports nothing on the ready channel. Before this
// change the state went to OFF and Relay exited.
func TestStatusIsNotOffAfterARejectedKey(t *testing.T) {
	handler := httphelpers.HandlerWithStatus(401)
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = time.Millisecond
		readyCh := p.streamManager.Start()

		require.Eventually(t, func() bool {
			return p.streamManager.Status().LastError.StatusCode == 401
		}, 2*time.Second, 10*time.Millisecond, "expected the rejection to be recorded")

		st := p.streamManager.Status()
		assert.NotEqual(t, interfaces.DataSourceStateOff, st.State,
			"the stream is still retrying, so it is not finished")
		// It never connected, so an interruption keeps the initializing state.
		assert.Equal(t, interfaces.DataSourceStateInitializing, st.State)
		assert.Equal(t, interfaces.DataSourceErrorKindErrorResponse, st.LastError.Kind)

		if !helpers.AssertNoMoreValues(t, readyCh, 300*time.Millisecond, "Relay reported a failure") {
			t.FailNow()
		}
	})
}

// Close is what makes the state terminal now.
func TestStatusIsOffAfterClose(t *testing.T) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamManagerTest(t, &initialEvent, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh
		p.streamManager.Close()
		assert.Equal(t, interfaces.DataSourceStateOff, p.streamManager.Status().State)
	})
}
