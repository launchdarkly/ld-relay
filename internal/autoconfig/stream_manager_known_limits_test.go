package autoconfig

// Tests in this file pin behavior Relay currently has and that we have decided not to change
// yet. They are not proofs of pending bugs: each one asserts today's behavior so that a future
// fix shows up as a failing test somebody has to update deliberately, rather than a silent
// change. The comment on each test says what the limitation is and why it is tolerated.

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"
)

// reconnectDelays returns every delay eventsource logged, from both the first-connection loop
// ("retrying in N secs") and the post-connection loop ("Reconnecting in N secs"). The library
// writes these through the eventsource logger bridge, which records them as slog messages.
func reconnectDelays(mockLog *logtest.MockHandler) []time.Duration {
	var out []time.Duration
	for _, e := range mockLog.EntriesForLevel(slog.LevelInfo) {
		for _, marker := range []string{"retrying in ", "Reconnecting in "} {
			_, after, found := strings.Cut(e.Message, marker)
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
		fmt.Fprintf(w, "retry: %d\nevent: put\ndata: {\"path\":\"/\",\"data\":{\"environments\":{}}}\n\n", hintMillis)
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	logger, mockLog := logtest.NewMockLogger()

	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", logger)
	require.NoError(t, err)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	sm := NewStreamManager(
		testConfigKey, serverURL, newTestMessageHandler(), httpConfig,
		time.Millisecond, rpacProtocolVersion, logger, noopTestCache{},
	)
	defer sm.Close()
	sm.extendedRetryDelay = 10 * time.Minute // the extended base delay we are supposed to get

	sm.Start()

	// Wait until the key has been rejected several times.
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&requestCount) >= 5
	}, 3*time.Second, 10*time.Millisecond, "the rejecting service was contacted fewer than 5 times")

	assert.True(t, mockLog.HasMessage(slog.LevelInfo, "engaging extended backoff"))
	delays := reconnectDelays(mockLog)
	t.Logf("delays eventsource used after the extended profile was engaged: %v", delays)
	for _, d := range delays {
		assert.Less(t, d, 5*time.Second,
			"every delay stayed on the 10ms hint, not the 10 min extended base")
	}
}
