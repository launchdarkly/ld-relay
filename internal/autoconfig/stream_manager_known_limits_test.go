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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
)

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
