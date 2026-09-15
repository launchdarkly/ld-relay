package autoconfig

// Tests in this file pin behavior Relay currently has and that we have decided not to change
// yet. They are not proofs of pending bugs: each one asserts today's behavior so that a future
// fix shows up as a failing test somebody has to update deliberately, rather than a silent
// change. The comment on each test says what the limitation is and why it is tolerated.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
)

// TestKnownLimitGiveUpUnreachableOnceTheStreamHasConnected shows the give-up predicate is only ever
// evaluated in the pre-connection wait loop (stream_manager.go:318-372). Once
// SubscribeWithRequestAndOptions has returned a stream, subscribe calls signalReady(nil) and
// enters consumeStream, and the authFailureCh send at stream_manager.go:625-628 has no reader
// forever after. A stream that connects with HTTP 200 but delivers no PUT therefore leaves
// Relay reporting a successful init with zero environments, and no later rejection of the key -
// however permanent - can make it report a failure.
func TestKnownLimitGiveUpUnreachableOnceTheStreamHasConnected(t *testing.T) {
	sseHandler, sseControl := httphelpers.SSEHandler(nil) // 200, no events
	defer sseControl.Close()
	handler := httphelpers.SequentialHandler(sseHandler, httphelpers.HandlerWithStatus(401))

	streamManagerTestWithStreamHandler(t, handler, sseControl, noopTestCache{}, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = time.Millisecond
		p.streamManager.initTimeout = 100 * time.Millisecond
		require.False(t, p.streamManager.ignoreConnectionErrors)

		readyCh := p.streamManager.Start()

		// Init "succeeded" on the bare HTTP 200, with no configuration of any kind.
		err := helpers.RequireValue(t, readyCh, 2*time.Second, "timed out waiting for the ready signal")
		require.NoError(t, err)
		p.mockLog.AssertMessageMatch(t, false, ldlog.Info, "Received configuration for")

		// Now the key is rejected on every reconnect, forever.
		sseControl.EndAll()

		require.Eventually(t, func() bool {
			return len(p.mockLog.GetOutput(ldlog.Error)) > 0 &&
				containsMatch(p.mockLog.GetOutput(ldlog.Error), "will keep retrying")
		}, 2*time.Second, 10*time.Millisecond, "expected the rejection to be logged")

		// No give-up, ever, even though Relay has nothing to serve and ignoreConnectionErrors is false.
		if !helpers.AssertNoMoreValues(t, readyCh, time.Second, "Relay reported a failure") {
			t.FailNow()
		}
		p.mockLog.AssertMessageMatch(t, false, ldlog.Error, "no cached configuration is available")
	})
}

func containsMatch(lines []string, sub string) bool {
	for _, l := range lines {
		if len(l) >= len(sub) {
			for i := 0; i+len(sub) <= len(l); i++ {
				if l[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// reconnectDelays returns every delay eventsource logged, from both the first-connection loop
// ("retrying in N secs") and the post-connection loop ("Reconnecting in N secs").
func reconnectDelays(mockLog *ldlogtest.MockLog) []time.Duration {
	var out []time.Duration
	for _, line := range mockLog.GetOutput(ldlog.Info) {
		for _, marker := range []string{"retrying in ", "Reconnecting in "} {
			_, after, found := strings.Cut(line, marker)
			if !found {
				continue
			}
			secs, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(after), " secs"), 64)
			if err == nil {
				out = append(out, time.Duration(secs*float64(time.Second)))
			}
		}
	}
	return out
}

// TestKnownLimitServerRetryHintDefeatsTheExtendedProfile: eventsource's baseDelayOverride (set by an
// SSE "retry:" field) replaces the active profile's base delay, including the extended
// profile's, and is never cleared (retry_delay.go NextRetryDelay: "if
// activeState.baseDelayOverride != nil { effectiveBase = *activeState.baseDelayOverride }").
// So once the stream has seen a retry hint, engaging the extended profile does not slow
// retries down to the intended 5 min: Relay keeps hammering the rejecting service on the
// hinted base delay instead.
func TestKnownLimitServerRetryHintDefeatsTheExtendedProfile(t *testing.T) {
	const hintMillis = 10
	var requestCount int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&requestCount, 1)
		if n > 1 {
			w.WriteHeader(401) // the key is rejected from now on
			return
		}
		// One good connection that carries a server-directed retry hint, then it drops.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprintf(w, "retry: %d\nevent: put\ndata: {\"path\":\"/\",\"data\":{\"environments\":{},\"filters\":{}}}\n\n", hintMillis)
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", mockLog.Loggers)
	require.NoError(t, err)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	sm := NewStreamManager(
		testConfigKey, serverURL, newTestMessageHandler(), httpConfig,
		time.Millisecond, rpacProtocolVersion, mockLog.Loggers, noopTestCache{},
		time.Second, true, // ignoreConnectionErrors so Relay stays up and keeps retrying
	)
	defer sm.Close()
	sm.extendedRetryDelay = 10 * time.Minute // the extended base delay we are supposed to get

	sm.Start()

	// Wait until the key has been rejected several times.
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt64(&requestCount) < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqual(t, atomic.LoadInt64(&requestCount), int64(5),
		"the rejecting service was contacted fewer than 5 times")

	mockLog.AssertMessageMatch(t, true, ldlog.Info, "engaging extended backoff")
	delays := reconnectDelays(mockLog)
	t.Logf("delays eventsource used after the extended profile was engaged: %v", delays)
	for _, d := range delays {
		assert.Less(t, d, 5*time.Second,
			"every delay stayed on the 10ms hint, not the 10 min extended base")
	}
}
