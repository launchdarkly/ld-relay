package autoconfig

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
)

// stacksContaining returns the stack of every goroutine whose trace mentions sub.
func stacksContaining(sub string) []string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	var out []string
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(g, sub) {
			out = append(out, g)
		}
	}
	return out
}

// newRejectingStreamManager points a StreamManager at a server that rejects every request, with
// an extended delay long enough that nothing can retry during the test. Any attempt made after
// Close() therefore has to come from a wait that shutdown failed to interrupt.
func newRejectingStreamManager(t *testing.T, extendedDelay time.Duration) (
	sm *StreamManager, mockLog *ldlogtest.MockLog, requests func() int, closeServer func(),
) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(401)
	}))

	mockLog = ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", mockLog.Loggers)
	require.NoError(t, err)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	sm = NewStreamManager(
		testConfigKey, serverURL, newTestMessageHandler(), httpConfig,
		time.Millisecond, rpacProtocolVersion, mockLog.Loggers, noopTestCache{},
		time.Second, true, // ignoreConnectionErrors: stay alive so shutdown is what ends it
	)
	sm.extendedRetryDelay = extendedDelay

	return sm, mockLog, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}, server.Close
}

// Close() must interrupt a pending backoff wait, not merely stop the next one from being
// scheduled. eventsource's retry loop selects on the request context and the delay timer and
// nothing else, so only cancelling the context can end a wait that has already started. With
// the extended delays running that wait is minutes long, so without this the goroutine outlives
// shutdown by minutes and then contacts LaunchDarkly again with a rejected credential.
func TestCloseInterruptsAPendingBackoffWait(t *testing.T) {
	sm, mockLog, _, closeServer := newRejectingStreamManager(t, 5*time.Minute)
	defer closeServer()
	defer mockLog.DumpIfTestFailed(t)

	sm.Start()

	// Wait until the first attempt has failed and the long wait is under way.
	require.Eventually(t, func() bool {
		return hasAnyMessage(mockLog, ldlog.Error, "Invalid auto-configuration key")
	}, 2*time.Second, 10*time.Millisecond, "expected the first rejection")

	sm.Close()

	require.Eventually(t, func() bool {
		return len(stacksContaining("eventsource.SubscribeWithRequestAndOptions")) == 0 &&
			len(stacksContaining("abandonStreamGoroutine")) == 0
	}, 2*time.Second, 20*time.Millisecond,
		"the stream goroutines must not outlive Close(); stacks still present: %v",
		append(stacksContaining("eventsource.SubscribeWithRequestAndOptions"),
			stacksContaining("abandonStreamGoroutine")...))
}

// The same guarantee stated in terms of observable behavior: nothing reaches LaunchDarkly after
// Close() returns. A short extended delay is used so a surviving wait would actually fire.
func TestNoRequestIsMadeAfterClose(t *testing.T) {
	sm, mockLog, requests, closeServer := newRejectingStreamManager(t, 200*time.Millisecond)
	defer closeServer()
	defer mockLog.DumpIfTestFailed(t)

	sm.Start()
	require.Eventually(t, func() bool { return requests() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"expected the first attempt")

	sm.Close()
	atClose := requests()

	// Several extended delays' worth of wall clock, so a surviving wait would have fired.
	time.Sleep(700 * time.Millisecond)
	assert.Equal(t, atClose, requests(), "Relay contacted the service after Close() returned")
}

// hasAnyMessage reports whether any line at the given level contains substr.
func hasAnyMessage(mockLog *ldlogtest.MockLog, level ldlog.LogLevel, substr string) bool {
	for _, line := range mockLog.GetOutput(level) {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}
